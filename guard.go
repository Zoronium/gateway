// Gateway hygiene: who may call it, from where, and how much at once.
package main

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log"
	"mime"
	"net"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"
)

type guard struct {
	key      string
	hosts    map[string]bool
	inflight chan struct{} // slots for model requests (chat, messages, responses) and for everything a virtual key calls
	control  chan struct{} // slots for the master key's admin calls and the admin page, so busy or hostile model traffic cannot lock the operator out; nil shares inflight
	perKey   int           // most requests one virtual key may have running at once; 0 means no cap
	running  *runCount
	keys     *keyring // virtual keys; nil when there is no database
}

// runCount counts running requests per key id.
type runCount struct {
	mu sync.Mutex
	n  map[string]int
}

func (c *runCount) acquire(id string, limit int) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.n[id] >= limit {
		return false
	}
	c.n[id]++
	return true
}

func (c *runCount) release(id string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.n[id]--; c.n[id] <= 0 {
		delete(c.n, id)
	}
}

// modelRoute reports whether the request asks a model for an answer: the calls that can run for minutes.
func modelRoute(r *http.Request) bool {
	if r.Method != http.MethodPost {
		return false
	}
	switch r.URL.Path {
	case "/v1/chat/completions", "/v1/messages", "/v1/responses":
		return true
	}
	return false
}

// ident says who is calling. The master key is "master". A virtual key carries its record, for limits.
type ident struct {
	ID  string
	Key *keyRec
}

func identOf(r *http.Request) ident {
	id, _ := r.Context().Value(identKey).(ident)
	return id
}

// virtual reports whether the caller is a virtual key. Those may only chat and list their own models;
// everything about the gateway itself is for the master key. A handler used without the guard sees no
// identity and is trusted, because in production the guard always sits in front.
func virtual(w http.ResponseWriter, r *http.Request) bool {
	if identOf(r).Key == nil {
		return false
	}
	jsonError(w, http.StatusForbidden, "admin_only", "This needs the master gateway key. Virtual keys can only call the chat, messages and responses endpoints and /v1/models.")
	return true
}

// jsonError answers in the OpenAI error shape, so SDKs show a readable message.
func jsonError(w http.ResponseWriter, status int, code, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(map[string]any{"error": map[string]any{"message": msg, "type": "gateway_error", "code": code}})
}

// hostOnly turns "Host: Example.com:8080" or "[::1]:8080" into "example.com" or "::1".
func hostOnly(hostport string) string {
	h := hostport
	if hh, _, err := net.SplitHostPort(hostport); err == nil {
		h = hh
	}
	return strings.ToLower(strings.Trim(h, "[]"))
}

func adminOnly(h http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !virtual(w, r) {
			h(w, r)
		}
	}
}

// loadGuard reads GATEWAY_KEY, ALLOWED_HOSTS and MAX_INFLIGHT. With no key set it makes one for
// this run and prints it, so a fresh install is never open.
func loadGuard(addr string) guard {
	key := os.Getenv("GATEWAY_KEY")
	switch {
	case key == "":
		b := make([]byte, 24)
		rand.Read(b)
		key = "gw-" + hex.EncodeToString(b)
		log.Printf("GATEWAY_KEY is not set. Using this key for this run only: %s", key)
		log.Printf("To keep it between runs, add the line GATEWAY_KEY=%s to .env", key)
	case len(key) < 16:
		log.Fatal("GATEWAY_KEY must be at least 16 characters")
	}
	hosts := map[string]bool{"localhost": true, "127.0.0.1": true, "::1": true}
	if h := hostOnly(addr); h != "" && h != "0.0.0.0" && h != "::" {
		hosts[h] = true
	}
	for _, h := range strings.Split(os.Getenv("ALLOWED_HOSTS"), ",") {
		if h = strings.ToLower(strings.TrimSpace(h)); h != "" {
			hosts[h] = true
		}
	}
	n := int(envFloat("MAX_INFLIGHT", 32))
	if n < 1 {
		log.Fatal("MAX_INFLIGHT must be at least 1")
	}
	ctl := int(envFloat("MAX_CONTROL_INFLIGHT", 8))
	perKey := int(envFloat("MAX_KEY_INFLIGHT", float64(max(1, n/4)))) // one key may take a quarter of the pool
	if ctl < 1 || perKey < 0 {
		log.Fatal("MAX_CONTROL_INFLIGHT must be at least 1 and MAX_KEY_INFLIGHT at least 0 (0 means no cap)")
	}
	return guard{key: key, hosts: hosts, inflight: make(chan struct{}, n), control: make(chan struct{}, ctl), perKey: perKey, running: &runCount{n: map[string]int{}}}
}

// wrap checks, in order and cheapest first: the Host header (blocks DNS rebinding), the key,
// the content type (a web page cannot send application/json to another origin without a
// preflight, which this gateway never approves), and how many requests are already running.
func (g guard) wrap(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !g.hosts[hostOnly(r.Host)] {
			log.Printf("denied: Host %q from %s", r.Host, r.RemoteAddr)
			jsonError(w, http.StatusForbidden, "host_not_allowed", "This Host header is not allowed. Set ALLOWED_HOSTS on the gateway to add it.")
			return
		}
		var who ident
		// The admin page itself is a static file with no data in it. It asks for the key and then calls the API.
		if !(r.Method == http.MethodGet && (r.URL.Path == "/admin" || r.URL.Path == "/admin/")) {
			got := ""
			if a := r.Header.Get("Authorization"); len(a) > 7 && strings.EqualFold(a[:7], "Bearer ") {
				got = a[7:]
			}
			if got == "" {
				got = r.Header.Get("x-api-key") // Anthropic-style clients send the key here
			}
			if subtle.ConstantTimeCompare([]byte(got), []byte(g.key)) == 1 {
				who = ident{ID: "master"}
			} else if got != "" && g.keys != nil {
				if rec, ok := g.keys.lookup(got); ok {
					who = ident{ID: rec.ID, Key: rec}
				}
			}
			if who.ID == "" {
				log.Printf("denied: bad or missing key from %s", r.RemoteAddr)
				w.Header().Set("WWW-Authenticate", "Bearer")
				jsonError(w, http.StatusUnauthorized, "invalid_api_key", "Missing or wrong gateway key. Send it as Authorization: Bearer <key>.")
				return
			}
			r = r.WithContext(context.WithValue(r.Context(), identKey, who))
		}
		if r.Method == http.MethodPost {
			if ct, _, err := mime.ParseMediaType(r.Header.Get("Content-Type")); err != nil || ct != "application/json" {
				jsonError(w, http.StatusUnsupportedMediaType, "unsupported_media_type", "Content-Type must be application/json.")
				return
			}
		}
		if r.ContentLength > maxBody { // refuse an announced oversize body before it takes a slot
			jsonError(w, http.StatusRequestEntityTooLarge, "body_too_large", fmt.Sprintf("The request body is over the gateway's limit of %d MB. Raise MAX_BODY_MB on the gateway.", maxBody>>20))
			return
		}
		// A virtual key may have only so many requests running, so one key cannot take the whole pool.
		if who.Key != nil && g.perKey > 0 && g.running != nil {
			if !g.running.acquire(who.ID, g.perKey) {
				w.Header().Set("Retry-After", "1")
				jsonError(w, http.StatusTooManyRequests, "key_busy", fmt.Sprintf("This key already has %d requests running. Wait for one to finish.", g.perKey))
				return
			}
			defer g.running.release(who.ID)
		}
		// The operator's own admin calls get their own slots. Model requests, and anything a virtual key calls, share the main pool.
		lane := g.inflight
		if g.control != nil && who.Key == nil && !modelRoute(r) {
			lane = g.control
		}
		select {
		case lane <- struct{}{}:
			defer func() { <-lane }()
		default:
			w.Header().Set("Retry-After", "1")
			jsonError(w, http.StatusTooManyRequests, "too_many_requests", "The gateway is busy. Retry shortly, or raise MAX_INFLIGHT.")
			return
		}
		next.ServeHTTP(w, r)
	})
}

// newServer sets the timeouts a bare http.ListenAndServe leaves out, so a slow client cannot hold
// connections open forever.
// ponytail: no server-wide WriteTimeout, because a streamed answer can run for minutes. Instead each write to the
// client gets its own deadline (statusWriter.Write) and a silent provider is cut off (idleBody).
func newServer(addr string, h http.Handler) *http.Server {
	return &http.Server{
		Addr:              addr,
		Handler:           h,
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       30 * time.Second,
		IdleTimeout:       120 * time.Second,
		MaxHeaderBytes:    64 << 10,
	}
}
