// Gateway hygiene: who may call it, from where, and how much at once.
package main

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"log"
	"mime"
	"net"
	"net/http"
	"os"
	"strings"
	"time"
)

type guard struct {
	key      string
	hosts    map[string]bool
	inflight chan struct{}
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
	return guard{key, hosts, make(chan struct{}, n)}
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
		got, _ := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
		if subtle.ConstantTimeCompare([]byte(got), []byte(g.key)) != 1 {
			log.Printf("denied: bad or missing key from %s", r.RemoteAddr)
			w.Header().Set("WWW-Authenticate", "Bearer")
			jsonError(w, http.StatusUnauthorized, "invalid_api_key", "Missing or wrong gateway key. Send it as Authorization: Bearer <GATEWAY_KEY>.")
			return
		}
		if r.Method == http.MethodPost {
			if ct, _, err := mime.ParseMediaType(r.Header.Get("Content-Type")); err != nil || ct != "application/json" {
				jsonError(w, http.StatusUnsupportedMediaType, "unsupported_media_type", "Content-Type must be application/json.")
				return
			}
		}
		select {
		case g.inflight <- struct{}{}:
			defer func() { <-g.inflight }()
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
// ponytail: no WriteTimeout, because a streamed answer can run for minutes.
func newServer(addr string, h http.Handler) *http.Server {
	return &http.Server{
		Addr:              addr,
		Handler:           h,
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       60 * time.Second,
		IdleTimeout:       120 * time.Second,
		MaxHeaderBytes:    64 << 10,
	}
}
