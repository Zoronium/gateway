package main

import (
	"bufio"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// Regression tests for the second review board's P0 findings: budgets that leak or overshoot, a control
// plane that model traffic could starve, and cut-off streams reported as finished.

// p0Stack is the real stack in front of one fake provider. Prices are $1000 per million tokens.
func p0Stack(t *testing.T, g guard, h http.HandlerFunc) (*httptest.Server, *store) {
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
	g.key, g.hosts, g.keys = testKey, map[string]bool{"127.0.0.1": true}, st.keys
	if g.running == nil {
		g.running = &runCount{n: map[string]int{}}
	}
	gw := httptest.NewServer(g.wrap(newHandler(map[string]provider{"fake": prov(t, fake, "k")}, policy{}, st)))
	t.Cleanup(gw.Close)
	return gw, st
}

// bg posts a chat request from a goroutine: it returns the status, or 0, and never calls t.Fatal.
func bg(gw *httptest.Server, key, body string) int {
	req, _ := http.NewRequest("POST", gw.URL+"/v1/chat/completions", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+key)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return 0
	}
	defer resp.Body.Close()
	io.Copy(io.Discard, resp.Body)
	return resp.StatusCode
}

// holdingProvider answers only after release is closed, so tests can keep requests running.
func holdingProvider() (http.HandlerFunc, func()) {
	release := make(chan struct{})
	var once sync.Once
	return func(w http.ResponseWriter, r *http.Request) {
			select {
			case <-release:
			case <-r.Context().Done():
				return
			}
			w.Write([]byte(`{"choices":[{"message":{"content":"hi"},"finish_reason":"stop"}],"usage":{"prompt_tokens":10,"completion_tokens":10}}`))
		},
		func() { once.Do(func() { close(release) }) }
}

// Requests running at the same time are each held at their worst case, so together they cannot pass the budget.
func TestConcurrentRequestsCannotOvershootBudget(t *testing.T) {
	h, release := holdingProvider()
	t.Cleanup(release)
	gw, st := p0Stack(t, guard{inflight: make(chan struct{}, 16)}, h)
	_, secret := newKey(t, gw, `{"name":"capped","budget_usd":0.1}`)

	codes := make(chan int, 6)
	for i := 0; i < 6; i++ {
		go func() {
			codes <- bg(gw, secret, small("fake/m")) // worst case about $0.031 each
		}()
	}
	for i := 0; i < 3; i++ { // 3 fit under 0.1, the other 3 are refused at once while those run
		select {
		case c := <-codes:
			if c != 403 {
				t.Fatalf("a request was let through past the budget: %d", c)
			}
		case <-time.After(3 * time.Second):
			t.Fatal("refusals did not arrive: the budget was not enforced while requests were running")
		}
	}
	release()
	for i := 0; i < 3; i++ {
		if c := <-codes; c != 200 {
			t.Fatalf("admitted request failed: %d", c)
		}
	}
	id := keyIDOf(t, gw)
	deadline := time.Now().Add(2 * time.Second)
	for st.keys.spentThisMonth(id) < 0.059 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if got := st.keys.spentThisMonth(id); got < 0.059 || got > 0.061 {
		t.Fatalf("spent = %v, want 0.06 (3 requests at 0.02)", got)
	}
	if n := len(st.keys.reserved); n != 0 {
		t.Fatalf("reservations were not released: %v", st.keys.reserved)
	}
}

// A request that could never fit the budget is refused before it is sent, with a message that says what to do.
func TestRequestWorseThanBudgetIsRefused(t *testing.T) {
	called := false
	gw, _ := p0Stack(t, guard{inflight: make(chan struct{}, 4)}, func(w http.ResponseWriter, r *http.Request) { called = true })
	_, secret := newKey(t, gw, `{"name":"capped","budget_usd":0.01}`)
	code, body := do(t, gw, "POST", "/v1/chat/completions", secret, small("fake/m"))
	if code != 403 || !strings.Contains(body, "max_tokens") || called {
		t.Fatalf("%d %s (provider called: %v)", code, body, called)
	}
}

// One slow reader of model traffic must not lock the operator out, and one key may not take the whole pool.
func TestAdminLaneAndPerKeyCap(t *testing.T) {
	h, release := holdingProvider()
	t.Cleanup(release)
	gw, _ := p0Stack(t, guard{inflight: make(chan struct{}, 2), control: make(chan struct{}, 2), perKey: 1}, h)
	_, a := newKey(t, gw, `{"name":"a"}`)
	_, b := newKey(t, gw, `{"name":"b"}`)

	go bg(gw, a, small("fake/m")) // a holds its one slot
	time.Sleep(200 * time.Millisecond)
	if code, body := do(t, gw, "POST", "/v1/chat/completions", a, small("fake/m")); code != 429 || !strings.Contains(body, "key_busy") {
		t.Fatalf("second request from the same key: %d %s", code, body)
	}
	go bg(gw, b, small("fake/m")) // b takes the other main slot: the pool is now full
	time.Sleep(200 * time.Millisecond)
	_, c := newKey(t, gw, `{"name":"c"}`)
	if code, _ := do(t, gw, "POST", "/v1/chat/completions", c, small("fake/m")); code != 429 {
		t.Fatalf("pool should be full for a third key, got %d", code)
	}
	// the operator can still list and revoke
	if code, body := do(t, gw, "GET", "/admin/api/keys", testKey, ""); code != 200 {
		t.Fatalf("admin locked out while the pool is full: %d %s", code, body)
	}
	if code, _ := do(t, gw, "GET", "/admin", "", ""); code != 200 {
		t.Fatalf("admin page blocked: %d", code)
	}
	release()
}

func TestBodyOverTheCapIsRefusedEarly(t *testing.T) {
	old := maxBody
	maxBody = 1 << 10
	t.Cleanup(func() { maxBody = old })
	gw, _ := p0Stack(t, guard{inflight: make(chan struct{}, 4)}, func(w http.ResponseWriter, r *http.Request) {})
	big := `{"model":"fake/m","messages":[{"role":"user","content":"` + strings.Repeat("x", 4096) + `"}]}`
	if code, body := do(t, gw, "POST", "/v1/chat/completions", testKey, big); code != 413 {
		t.Fatalf("%d %s", code, body)
	}
}

// truncatedProvider sends some text and then closes the stream with no finish_reason and no [DONE].
func truncatedProvider(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/event-stream")
	w.Write([]byte("data: {\"choices\":[{\"delta\":{\"content\":\"half a sent\"}}]}\n\n"))
}

func TestTruncatedStreamIsNotReportedAsFinished(t *testing.T) {
	gw, st := p0Stack(t, guard{inflight: make(chan struct{}, 4)}, truncatedProvider)
	cases := []struct{ path, body, ok, fail string }{
		{"/v1/messages", `{"model":"fake/m","max_tokens":9,"stream":true,"messages":[{"role":"user","content":"hi"}]}`, "message_stop", "event: error"},
		{"/v1/responses", `{"model":"fake/m","stream":true,"input":"hi"}`, "response.completed", "response.failed"},
	}
	for _, c := range cases {
		code, out := do(t, gw, "POST", c.path, testKey, c.body)
		if code != 200 || strings.Contains(out, c.ok) || !strings.Contains(out, c.fail) {
			t.Fatalf("%s: %d, a cut-off stream looked finished:\n%s", c.path, code, out)
		}
	}
	// the plain chat route passes bytes through, so it can only record it
	do(t, gw, "POST", "/v1/chat/completions", testKey, `{"model":"fake/m","stream":true,"messages":[]}`)
	st.flush()
	_, out := do(t, gw, "GET", "/admin/api/events?since=1h", testKey, "")
	if !strings.Contains(out, `"result":"provider_error"`) {
		t.Fatalf("a cut-off stream was logged as ok: %s", out)
	}
	// a stream that ends properly still works
	ok, _ := p0Stack(t, guard{inflight: make(chan struct{}, 4)}, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.Write([]byte("data: {\"choices\":[{\"delta\":{\"content\":\"hi\"},\"finish_reason\":\"stop\"}]}\n\n"))
	})
	if _, out := do(t, ok, "POST", "/v1/messages", testKey, cases[0].body); !strings.Contains(out, "message_stop") || strings.Contains(out, "event: error") {
		t.Fatalf("a finish_reason alone should end the stream cleanly:\n%s", out)
	}
}

// A provider that goes quiet mid-answer is cut off, so it cannot hold a slot for ever.
func TestStalledProviderIsCutOff(t *testing.T) {
	old := streamIdle
	streamIdle = 300 * time.Millisecond
	t.Cleanup(func() { streamIdle = old })
	gw, _ := p0Stack(t, guard{inflight: make(chan struct{}, 4)}, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.Write([]byte("data: {\"choices\":[{\"delta\":{\"content\":\"hi\"}}]}\n\n"))
		w.(http.Flusher).Flush()
		<-r.Context().Done() // never finishes on its own
	})
	req, _ := http.NewRequest("POST", gw.URL+"/v1/chat/completions", strings.NewReader(`{"model":"fake/m","stream":true,"messages":[]}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+testKey)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	done := make(chan struct{})
	go func() { io.Copy(io.Discard, bufio.NewReader(resp.Body)); close(done) }()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("a silent provider held the request open")
	}
}
