// Stage 2: OpenAI-compatible gateway with several providers and fallback.
// The client picks the provider in the model field: "groq/openai/gpt-oss-120b".
// Comma-separated models are a fallback chain: "groq/x,nvidia/y".
package main

import (
	"bytes"
	"context"
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
	"strconv"
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
	mux.HandleFunc("GET /v1/providers", adminOnly(info.listProviders))
	mux.HandleFunc("GET /v1/stats", adminOnly(info.getStats))
	newAdmin(st, ps, pol).routes(mux)
	// ponytail: only chat completions. The key, host and size checks live in guard.go and wrap this handler.
	// A dialect is another API format (Anthropic Messages, OpenAI Responses): the body is translated to a chat
	// body first, so the filter, limits and fallback all apply, and a good answer is translated back. nil is chat.
	chatFor := func(d *dialect) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			ri, who := infoOf(r), identOf(r)
			reqID := ri.id
			if who.Key != nil && st.keys.overBudget(who.Key) {
				ri.result = "over_budget"
				jsonError(w, http.StatusForbidden, "budget_exceeded", fmt.Sprintf("This key has used its monthly budget of $%s. It resets on the 1st (UTC) or when an admin raises it (request %s).", strconv.FormatFloat(who.Key.BudgetUSD, 'f', -1, 64), reqID))
				return
			}
			if who.Key != nil && who.Key.RPM > 0 {
				if ok, wait := st.keys.allow(who.Key.ID, who.Key.RPM); !ok {
					ri.result = "rate_limited"
					w.Header().Set("Retry-After", fmt.Sprint(int(wait.Seconds())+1))
					jsonError(w, http.StatusTooManyRequests, "rate_limit_exceeded", fmt.Sprintf("This key is limited to %d requests per minute. Retry shortly (request %s).", who.Key.RPM, reqID))
					return
				}
			}
			raw, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxBody))
			if err != nil {
				ri.result = "too_large"
				http.Error(w, "request body too large or unreadable", http.StatusRequestEntityTooLarge)
				return
			}
			if d != nil {
				var err error
				if raw, err = d.in(raw); err != nil {
					ri.result = "bad_request"
					http.Error(w, "body must be a JSON "+d.name+" request", http.StatusBadRequest)
					return
				}
			}
			var body map[string]json.RawMessage
			var model string
			if json.Unmarshal(raw, &body) != nil || json.Unmarshal(body["model"], &model) != nil || model == "" {
				ri.result = "bad_request"
				http.Error(w, `body must be JSON with a "model" like provider/model`, http.StatusBadRequest)
				return
			}
			ri.model = model
			ri.estIn = len(raw)/4 + 1
			if len(ri.model) > 200 {
				ri.model = ri.model[:200]
			}
			texts := newTexts(body["messages"], pol.scanSystem)
			if pol.maxScan > 0 && len(pol.filters) > 0 {
				// Fail closed: unscanned text must not be a way past the filter, so too much new text is refused.
				total := 0
				for _, t := range texts {
					total += len(t)
				}
				if total > pol.maxScan {
					ri.result = "too_large"
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
			ri.v = v
			if v.filter != "" && v.score >= pol.log {
				if v.score >= pol.block {
					ri.result = "blocked"
					log.Printf("req %s BLOCK filter=%s rule=%s score=%.2f", reqID, v.filter, v.rule, v.score)
					blocked(w, reqID, v)
					return
				}
				ri.flagged = true
				log.Printf("req %s FLAG filter=%s rule=%s score=%.2f", reqID, v.filter, v.rule, v.score)
				w.Header().Set("X-Gateway-Flagged", fmt.Sprintf("%s=%.2f", v.filter, v.score))
			}
			var targets []target
			for _, c := range strings.Split(model, ",") {
				name, m, ok := strings.Cut(strings.TrimSpace(c), "/")
				p, found := ps[name]
				if !ok || !found || m == "" {
					ri.result = "bad_request"
					http.Error(w, fmt.Sprintf("unknown provider in %q, use provider/model, configured: %s", c, names(ps)), http.StatusBadRequest)
					return
				}
				if who.Key != nil && !who.Key.allows(name, m) {
					ri.result = "model_denied"
					jsonError(w, http.StatusForbidden, "model_not_allowed", fmt.Sprintf("This key may not use %s/%s (request %s).", name, m, reqID))
					return
				}
				if who.Key != nil && who.Key.BudgetUSD > 0 {
					if _, priced := priceOf(name + "/" + m); !priced {
						ri.result = "model_denied"
						jsonError(w, http.StatusForbidden, "model_unpriced", fmt.Sprintf("This key has a budget, and %s/%s has no price in PRICES_FILE, so its cost cannot be counted (request %s).", name, m, reqID))
						return
					}
				}
				targets = append(targets, target{name, m, p})
			}
			var stream bool
			json.Unmarshal(body["stream"], &stream)
			if stream {
				// Always ask the provider to report tokens, whatever the client sent, so a client cannot switch spend counting off.
				opts := map[string]json.RawMessage{}
				json.Unmarshal(body["stream_options"], &opts)
				if opts == nil { // the client sent null
					opts = map[string]json.RawMessage{}
				}
				opts["include_usage"] = json.RawMessage(`true`)
				body["stream_options"], _ = json.Marshal(opts)
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
				ri.provider, ri.ttfb = t.name, ttfb
				switch {
				case err != nil:
					log.Printf("%s/%s unreachable: %v", t.name, t.model, err)
					st.record(attempt{time.Now(), t.name, t.model, 0, classify(0, err), ttfb})
					if last {
						ri.result = "provider_error"
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
				var broke bool
				if d != nil && resp.StatusCode == http.StatusOK {
					broke = d.pass(w, resp, t, ri, stream)
				} else {
					broke = pass(w, resp, t, ri)
				}
				if class == "ok" {
					ri.used = t.name + "/" + t.model
				}
				if broke && class == "ok" {
					class = "stream_error" // the provider said 200 and then dropped the stream
				}
				if class != "ok" {
					ri.result = "provider_error"
				}
				st.record(attempt{time.Now(), t.name, t.model, resp.StatusCode, class, ttfb})
				return
			}
		}
	}
	mux.Handle("POST /v1/chat/completions", logged(st, chatFor(nil)))
	mux.Handle("POST /v1/messages", logged(st, chatFor(&dialect{"Messages", anthropicToChat, anthropicPass})))
	mux.Handle("POST /v1/responses", logged(st, chatFor(&dialect{"Responses", responsesToChat, responsesPass})))
	mux.HandleFunc("POST /v1/messages/count_tokens", countTokens)
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
func pass(w http.ResponseWriter, resp *http.Response, t target, ri *reqInfo) (providerBroke bool) {
	defer resp.Body.Close()
	var tail usageTail
	defer func() { ri.inTok, ri.outTok = tail.tokens() }()
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
			tail.write(buf[:n])
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
	if pf := os.Getenv("PRICES_FILE"); pf != "" {
		var err error
		if prices, err = loadPrices(pf); err != nil {
			log.Fatal(err)
		}
		log.Printf("prices: %d entries from %s", len(prices), pf)
	}
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
	keepDays := int(envFloat("STATS_DAYS", 30))
	st, err := openStore(dbPath, keepDays)
	if err != nil {
		// The proxy matters more than its stats, so it keeps running; /v1/models and friends answer 503.
		// Without a database there are no virtual keys and no admin data, only the master key.
		log.Printf("stats are off: %v", err)
		st = nil
	} else {
		log.Printf("stats in %s, kept %d days", dbPath, keepDays)
		g.keys = st.keys
		startRefresher(ps, st, time.Duration(envFloat("MODELS_REFRESH_MIN", 60))*time.Minute)
	}
	spec := os.Getenv("ACCESS_LOG")
	if access, err = openAccessLog(spec); err != nil {
		log.Fatal(err)
	} else if access != nil {
		log.Printf("access log (one JSON line per request) to %s", spec)
	}
	var hosts []string
	for h := range g.hosts {
		hosts = append(hosts, h)
	}
	sort.Strings(hosts)
	runtimeInfo = serverInfo{Listen: addr, MaxInflight: cap(g.inflight), Hosts: hosts, DBPath: dbPath, KeepDays: keepDays, AccessLog: spec, Started: time.Now()}
	log.Printf("admin page: http://%s/admin (sign in with GATEWAY_KEY)", addr)
	log.Fatal(newServer(addr, g.wrap(newHandler(ps, pol, st))).ListenAndServe())
}

type dialect struct {
	name string
	in   func(raw []byte) ([]byte, error)
	pass func(w http.ResponseWriter, resp *http.Response, t target, ri *reqInfo, stream bool) (providerBroke bool)
}
