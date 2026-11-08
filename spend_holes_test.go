package main

import (
	"bufio"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// Regression tests for holes the review board found in spend counting and key creation.

// holesStack is the real stack in front of one fake provider, with $1000 per million tokens so any
// counted token is visible. h is the provider.
func holesStack(t *testing.T, h http.HandlerFunc) (*httptest.Server, *store) {
	t.Helper()
	prices = map[string]price{"fake/m": {1000, 1000}}
	t.Cleanup(func() { prices = nil })
	fake := httptest.NewServer(h)
	t.Cleanup(fake.Close)
	st, err := openStore(filepath.Join(t.TempDir(), "t.db"), 30)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(st.close)
	g := guard{key: testKey, hosts: map[string]bool{"127.0.0.1": true}, inflight: make(chan struct{}, 8), keys: st.keys}
	gw := httptest.NewServer(g.wrap(newHandler(map[string]provider{"fake": prov(t, fake, "k")}, policy{}, st)))
	t.Cleanup(gw.Close)
	return gw, st
}

func TestStreamOptionsCannotSwitchOffUsage(t *testing.T) {
	var seen map[string]json.RawMessage
	gw, _ := holesStack(t, func(w http.ResponseWriter, r *http.Request) {
		json.NewDecoder(r.Body).Decode(&seen)
		w.Write([]byte("data: [DONE]\n\n"))
	})
	for _, opts := range []string{`{"include_usage":false,"keep":1}`, `null`, `"junk"`, `{}`} {
		seen = nil
		do(t, gw, "POST", "/v1/chat/completions", testKey, `{"model":"fake/m","stream":true,"stream_options":`+opts+`,"messages":[]}`)
		var got map[string]json.RawMessage
		json.Unmarshal(seen["stream_options"], &got)
		if string(got["include_usage"]) != "true" {
			t.Fatalf("stream_options %s reached the provider as %s", opts, seen["stream_options"])
		}
	}
	// other options the client set are kept
	seen = nil
	do(t, gw, "POST", "/v1/chat/completions", testKey, `{"model":"fake/m","stream":true,"stream_options":{"include_usage":false,"keep":1},"messages":[]}`)
	var got map[string]json.RawMessage
	json.Unmarshal(seen["stream_options"], &got)
	if string(got["keep"]) != "1" {
		t.Fatalf("client's other stream option was dropped: %s", seen["stream_options"])
	}
}

// A client that reads one chunk and hangs up must still be charged, not booked as free.
func TestAbortedStreamIsCharged(t *testing.T) {
	for _, path := range []string{"/v1/chat/completions", "/v1/messages", "/v1/responses"} {
		t.Run(path, func(t *testing.T) {
			gw, st := holesStack(t, func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "text/event-stream")
				for i := 0; i < 40; i++ { // slow enough that the client is gone before the usage chunk
					w.Write([]byte("data: {\"choices\":[{\"delta\":{\"content\":\"x\"}}]}\n\n"))
					w.(http.Flusher).Flush()
					time.Sleep(25 * time.Millisecond)
				}
				w.Write([]byte("data: {\"choices\":[],\"usage\":{\"prompt_tokens\":9,\"completion_tokens\":9}}\n\ndata: [DONE]\n\n"))
			})
			_, secret := newKey(t, gw, `{"name":"capped","budget_usd":100}`)
			id := keyIDOf(t, gw)

			body := `{"model":"fake/m","stream":true,"input":"hello","max_tokens":50,"messages":[{"role":"user","content":"hello there, this is a prompt"}]}`
			req, _ := http.NewRequest("POST", gw.URL+path, strings.NewReader(body))
			req.Header.Set("Content-Type", "application/json")
			req.Header.Set("Authorization", "Bearer "+secret)
			resp, err := http.DefaultClient.Do(req)
			if err != nil {
				t.Fatal(err)
			}
			bufio.NewReader(resp.Body).ReadString('\n') // one line, then hang up
			resp.Body.Close()

			deadline := time.Now().Add(3 * time.Second)
			for st.keys.spentThisMonth(id) == 0 && time.Now().Before(deadline) {
				time.Sleep(20 * time.Millisecond)
			}
			if st.keys.spentThisMonth(id) == 0 {
				t.Fatal("an aborted stream was booked as free")
			}
		})
	}
}

func TestKeyCreationRejectsUnknownFields(t *testing.T) {
	gw, _ := holesStack(t, func(w http.ResponseWriter, r *http.Request) {})
	for _, body := range []string{`{"name":"alice","budget":10}`, `{"name":"alice","budgetUSD":10}`, `{"name":"alice","rpm":5,"extra":true}`} {
		code, out := do(t, gw, "POST", "/admin/api/keys", testKey, body)
		if code != 400 || !strings.Contains(out, "unknown field") {
			t.Fatalf("%s: %d %s", body, code, out)
		}
	}
	if _, out := do(t, gw, "GET", "/admin/api/keys", testKey, ""); strings.Contains(out, "alice") {
		t.Fatalf("a rejected request still created a key: %s", out)
	}
	if code, out := do(t, gw, "POST", "/admin/api/keys", testKey, `{"name":"alice","budget_usd":10}`); code != 201 {
		t.Fatalf("valid body refused: %d %s", code, out)
	}
}

func TestBearerPrefixIsCaseInsensitive(t *testing.T) {
	gw, _ := holesStack(t, func(w http.ResponseWriter, r *http.Request) {})
	for _, prefix := range []string{"Bearer ", "bearer ", "BEARER "} {
		req, _ := http.NewRequest("GET", gw.URL+"/v1/models", nil)
		req.Header.Set("Authorization", prefix+testKey)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if resp.StatusCode != 200 {
			t.Fatalf("%q: status %d", prefix, resp.StatusCode)
		}
	}
}
