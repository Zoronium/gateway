// Stage 2: OpenAI-compatible gateway with several providers and fallback.
// The client picks the provider in the model field: "groq/openai/gpt-oss-120b".
// Comma-separated models are a fallback chain: "groq/x,nvidia/y".
package main

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"os"
	"runtime"
	"sort"
	"strings"
	"time"
)

const maxBody = 10 << 20

type provider struct {
	base *url.URL
	key  string
}

// All of these speak the OpenAI chat format, so no translation is needed.
// ponytail: Anthropic's compat endpoint is for testing per Anthropic, add a real translator if you rely on it.
var known = []struct{ name, base, keyEnv string }{
	{"openai", "https://api.openai.com/v1", "OPENAI_API_KEY"},
	{"anthropic", "https://api.anthropic.com/v1", "ANTHROPIC_API_KEY"},
	{"openrouter", "https://openrouter.ai/api/v1", "OPENROUTER_API_KEY"},
	{"groq", "https://api.groq.com/openai/v1", "GROQ_API_KEY"},
	{"nvidia", "https://integrate.api.nvidia.com/v1", "NVIDIA_API_KEY"},
	{"mistral", "https://api.mistral.ai/v1", "MISTRAL_API_KEY"},
	{"huggingface", "https://router.huggingface.co/v1", "HUGGINGFACE_API_KEY"},
	{"ollama", "http://127.0.0.1:11434/v1", ""},
}

func loadProviders() map[string]provider {
	ps := map[string]provider{}
	for _, k := range known {
		key := ""
		if k.keyEnv != "" {
			key = os.Getenv(k.keyEnv)
		}
		base := os.Getenv(strings.ToUpper(k.name) + "_BASE_URL")
		if key == "" && !(k.name == "ollama" && base != "") {
			continue // a provider is on only when its key is set (ollama: when its URL is set)
		}
		if base == "" {
			base = k.base
		}
		u, err := url.Parse(base)
		if err != nil || u.Host == "" {
			log.Fatalf("bad base URL for %s", k.name)
		}
		ps[k.name] = provider{u, key}
	}
	return ps
}

type target struct {
	name, model string
	p           provider
}

func names(ps map[string]provider) string {
	var n []string
	for k := range ps {
		n = append(n, k)
	}
	sort.Strings(n)
	return strings.Join(n, ", ")
}

// newHandler builds the gateway. st may be nil: the proxy then works without stats.
func newHandler(ps map[string]provider, pol policy, st *store) http.Handler {
	tr := http.DefaultTransport.(*http.Transport).Clone()
	tr.ResponseHeaderTimeout = 60 * time.Second // a stalled free model moves on to the next one
	client := &http.Client{Transport: tr}
	scanSem := make(chan struct{}, runtime.NumCPU())

	mux := http.NewServeMux()
	info := newAPI(ps, st, client)
	mux.HandleFunc("GET /v1/models", info.listModels)
	mux.HandleFunc("GET /v1/providers", info.listProviders)
	mux.HandleFunc("GET /v1/stats", info.getStats)
	// ponytail: only chat completions. The key, host and size checks live in guard.go and wrap this handler.
	mux.HandleFunc("POST /v1/chat/completions", func(w http.ResponseWriter, r *http.Request) {
		raw, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxBody))
		if err != nil {
			http.Error(w, "request body too large or unreadable", http.StatusRequestEntityTooLarge)
			return
		}
		var body map[string]json.RawMessage
		var model string
		if json.Unmarshal(raw, &body) != nil || json.Unmarshal(body["model"], &model) != nil || model == "" {
			http.Error(w, `body must be JSON with a "model" like provider/model`, http.StatusBadRequest)
			return
		}
		id := make([]byte, 6)
		rand.Read(id)
		reqID := hex.EncodeToString(id)
		w.Header().Set("X-Gateway-Request-Id", reqID)
		texts := newTexts(body["messages"], pol.scanSystem)
		if pol.maxScan > 0 && len(pol.filters) > 0 {
			// Fail closed: unscanned text must not be a way past the filter, so too much new text is refused.
			total := 0
			for _, t := range texts {
				total += len(t)
			}
			if total > pol.maxScan {
				log.Printf("req %s refused: %d bytes of new text is over the scan limit", reqID, total)
				jsonError(w, http.StatusRequestEntityTooLarge, "scan_limit_exceeded", fmt.Sprintf("New content is %d KB, over the gateway's scan limit of %d KB, so it was not sent on (request %s). Raise SCAN_LIMIT_MB, or set FILTERS=none on the gateway.", total>>10, pol.maxScan>>10, reqID))
				return
			}
		}
		var v verdict
		if len(pol.filters) > 0 {
			select { // at most one scan per CPU at a time, so a pile of big requests cannot starve the machine
			case scanSem <- struct{}{}:
			case <-r.Context().Done():
				return
			}
			v = pol.check(texts)
			<-scanSem
		}
		if v.filter != "" && v.score >= pol.log {
			if v.score >= pol.block {
				log.Printf("req %s BLOCK filter=%s rule=%s score=%.2f", reqID, v.filter, v.rule, v.score)
				blocked(w, reqID, v)
				return
			}
			log.Printf("req %s FLAG filter=%s rule=%s score=%.2f", reqID, v.filter, v.rule, v.score)
			w.Header().Set("X-Gateway-Flagged", fmt.Sprintf("%s=%.2f", v.filter, v.score))
		}
		var targets []target
		for _, c := range strings.Split(model, ",") {
			name, m, ok := strings.Cut(strings.TrimSpace(c), "/")
			p, found := ps[name]
			if !ok || !found || m == "" {
				http.Error(w, fmt.Sprintf("unknown provider in %q, use provider/model, configured: %s", c, names(ps)), http.StatusBadRequest)
				return
			}
			targets = append(targets, target{name, m, p})
		}
		for i, t := range targets {
			last := i == len(targets)-1
			b, _ := json.Marshal(t.model)
			body["model"] = b
			out, _ := json.Marshal(body)
			req, _ := http.NewRequestWithContext(r.Context(), "POST", t.p.base.JoinPath("chat/completions").String(), bytes.NewReader(out))
			req.Header.Set("Content-Type", "application/json")
			if a := r.Header.Get("Accept"); a != "" {
				req.Header.Set("Accept", a)
			}
			if t.p.key != "" {
				req.Header.Set("Authorization", "Bearer "+t.p.key) // client key never reaches a provider
			}
			start := time.Now()
			resp, err := client.Do(req)
			ttfb := time.Since(start).Milliseconds()
			switch {
			case err != nil:
				log.Printf("%s/%s unreachable: %v", t.name, t.model, err)
				st.record(attempt{time.Now(), t.name, t.model, 0, classify(0, err), ttfb})
				if last {
					http.Error(w, "provider unreachable: "+t.name, http.StatusBadGateway)
					return
				}
				continue
			case (resp.StatusCode == 429 || resp.StatusCode >= 500) && !last:
				log.Printf("%s/%s returned %d, trying next", t.name, t.model, resp.StatusCode)
				st.record(attempt{time.Now(), t.name, t.model, resp.StatusCode, classify(resp.StatusCode, nil), ttfb})
				resp.Body.Close()
				continue
			}
			class := classify(resp.StatusCode, nil)
			if broke := pass(w, resp, t); broke && class == "ok" {
				class = "stream_error" // the provider said 200 and then dropped the stream
			}
			st.record(attempt{time.Now(), t.name, t.model, resp.StatusCode, class, ttfb})
			return
		}
	})
	return mux
}

// blocked answers 403 in the OpenAI error shape. 403 is a client error, so agents should not retry it.
// The matched rule and the score go to the log only, so a client cannot probe the filter for them.
func blocked(w http.ResponseWriter, reqID string, v verdict) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusForbidden)
	json.NewEncoder(w).Encode(map[string]any{"error": map[string]any{
		"message": fmt.Sprintf("Blocked by gateway filter %q (request %s). If this is a false alarm, raise FILTER_BLOCK or set FILTERS=none on the gateway.", v.filter, reqID),
		"type":    "gateway_filter_blocked",
		"code":    "blocked_by_filter", "filter": v.filter, "request_id": reqID,
	}})
}

// pass streams the provider response to the client, flushing every read so SSE arrives live.
// It reports true when the provider's stream broke. A client hanging up is not the provider's fault.
func pass(w http.ResponseWriter, resp *http.Response, t target) (providerBroke bool) {
	defer resp.Body.Close()
	for k, v := range resp.Header {
		switch http.CanonicalHeaderKey(k) {
		case "Connection", "Keep-Alive", "Transfer-Encoding", "Set-Cookie": // provider cookies must not reach the client
		default:
			w.Header()[k] = v
		}
	}
	w.Header().Set("X-Gateway-Provider", t.name+"/"+t.model)
	w.WriteHeader(resp.StatusCode)
	rc := http.NewResponseController(w)
	buf := make([]byte, 4096)
	for {
		n, err := resp.Body.Read(buf)
		if n > 0 {
			if _, werr := w.Write(buf[:n]); werr != nil {
				return false
			}
			rc.Flush()
		}
		if err != nil {
			return err != io.EOF && !errors.Is(err, context.Canceled)
		}
	}
}

// loadDotenv reads KEY=value lines. Real environment variables win over the file.
// ponytail: no multiline values or variable expansion, add only if a real use shows up.
func loadDotenv(path string) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return // no .env is fine
	}
	for _, line := range strings.Split(string(raw), "\n") {
		line = strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(line), "export "))
		k, v, ok := strings.Cut(line, "=")
		k, v = strings.TrimSpace(k), strings.TrimSpace(v)
		if !ok || k == "" || strings.HasPrefix(k, "#") {
			continue
		}
		if len(v) >= 2 && (v[0] == '"' || v[0] == '\'') && v[len(v)-1] == v[0] {
			v = v[1 : len(v)-1]
		}
		if _, set := os.LookupEnv(k); !set {
			os.Setenv(k, v)
		}
	}
}

func main() {
	loadDotenv(".env")
	ps := loadProviders()
	if len(ps) == 0 {
		log.Fatal("no providers: set at least one key such as GROQ_API_KEY, or OLLAMA_BASE_URL")
	}
	addr := os.Getenv("LISTEN")
	if addr == "" {
		addr = "127.0.0.1:8080" // localhost only unless you choose otherwise
	}
	pol := loadPolicy()
	g := loadGuard(addr)
	var fs []string
	for _, f := range pol.filters {
		fs = append(fs, f.name)
	}
	log.Printf("gateway on %s, providers: %s, filters: %s (log %.2f, block %.2f)", addr, names(ps), strings.Join(fs, ","), pol.log, pol.block)
	dbPath := os.Getenv("DB_PATH")
	if dbPath == "" {
		dbPath = "gateway.db"
	}
	st, err := openStore(dbPath, int(envFloat("STATS_DAYS", 30)))
	if err != nil {
		// The proxy matters more than its stats, so it keeps running; /v1/models and friends answer 503.
		log.Printf("stats are off: %v", err)
		st = nil
	} else {
		log.Printf("stats in %s, kept %d days", dbPath, int(envFloat("STATS_DAYS", 30)))
		startRefresher(ps, st, time.Duration(envFloat("MODELS_REFRESH_MIN", 60))*time.Minute)
	}
	log.Fatal(newServer(addr, g.wrap(newHandler(ps, pol, st))).ListenAndServe())
}
