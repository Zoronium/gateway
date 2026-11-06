package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
)

// spendStack is the real stack with a fake provider that reports 10 prompt and 10 completion tokens.
// At $1000 per million tokens each request costs $0.02.
func spendStack(t *testing.T, dbPath string) (*httptest.Server, *store, *string) {
	t.Helper()
	prices = map[string]price{"fake/m": {1000, 1000}, "fake/free*": {0, 0}}
	t.Cleanup(func() { prices = nil })
	seen := new(string)
	fake := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var b map[string]json.RawMessage
		json.NewDecoder(r.Body).Decode(&b)
		*seen = string(b["stream_options"])
		if string(b["stream"]) == "true" {
			w.Header().Set("Content-Type", "text/event-stream")
			w.Write([]byte("data: {\"choices\":[{\"delta\":{\"content\":\"hi\"}}]}\n\ndata: {\"choices\":[],\"usage\":{\"prompt_tokens\":10,\"completion_tokens\":10}}\n\ndata: [DONE]\n\n"))
			return
		}
		w.Write([]byte(`{"choices":[{"message":{"content":"hi"},"finish_reason":"stop"}],"usage":{"prompt_tokens":10,"completion_tokens":10}}`))
	}))
	t.Cleanup(fake.Close)
	st, err := openStore(dbPath, 30)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(st.close)
	g := guard{key: testKey, hosts: map[string]bool{"127.0.0.1": true}, inflight: make(chan struct{}, 8), keys: st.keys}
	gw := httptest.NewServer(g.wrap(newHandler(map[string]provider{"fake": prov(t, fake, "k")}, policy{}, st)))
	t.Cleanup(gw.Close)
	return gw, st, seen
}

func TestBudgetStopsAKey(t *testing.T) {
	gw, st, _ := spendStack(t, filepath.Join(t.TempDir(), "t.db"))
	_, secret := newKey(t, gw, `{"name":"capped","budget_usd":0.03}`)

	for i, want := range []int{200, 200, 403} { // 0.02, then 0.04 (over 0.03), then refused
		code, body := do(t, gw, "POST", "/v1/chat/completions", secret, chat("fake/m"))
		if code != want {
			t.Fatalf("request %d: status %d, want %d: %s", i+1, code, want, body)
		}
		if want == 403 && !strings.Contains(body, "budget_exceeded") {
			t.Fatalf("refusal = %s", body)
		}
	}
	if got := st.keys.spentThisMonth(strings.TrimSpace(keyIDOf(t, gw))); got < 0.039 || got > 0.041 {
		t.Fatalf("spent = %v, want 0.04", got)
	}
	// the master key has no budget
	if code, _ := do(t, gw, "POST", "/v1/chat/completions", testKey, chat("fake/m")); code != 200 {
		t.Fatalf("master blocked: %d", code)
	}
	st.flush()
	_, out := do(t, gw, "GET", "/admin/api/events?since=1h&key="+keyIDOf(t, gw), testKey, "")
	var ev struct{ Data []event }
	json.Unmarshal([]byte(out), &ev)
	if len(ev.Data) != 3 || ev.Data[2].InTok != 10 || ev.Data[2].OutTok != 10 || ev.Data[2].CostUSD != 0.02 || ev.Data[0].Result != "over_budget" {
		t.Fatalf("events = %s", out)
	}
	_, sum := do(t, gw, "GET", "/admin/api/summary?since=1h", testKey, "")
	if !strings.Contains(sum, `"cost_usd":0.06`) {
		t.Fatalf("summary cost: %s", sum)
	}
}

func keyIDOf(t *testing.T, gw *httptest.Server) string {
	_, out := do(t, gw, "GET", "/admin/api/keys", testKey, "")
	var k struct{ Data []struct{ ID string } }
	json.Unmarshal([]byte(out), &k)
	return k.Data[0].ID
}

func TestBudgetNeedsAPrice(t *testing.T) {
	gw, _, _ := spendStack(t, filepath.Join(t.TempDir(), "t.db"))
	_, secret := newKey(t, gw, `{"name":"capped","budget_usd":5}`)
	if code, body := do(t, gw, "POST", "/v1/chat/completions", secret, chat("fake/unlisted")); code != 403 || !strings.Contains(body, "model_unpriced") {
		t.Fatalf("unpriced model: %d %s", code, body)
	}
	if code, body := do(t, gw, "POST", "/v1/chat/completions", secret, chat("fake/free-one")); code != 200 {
		t.Fatalf("free pattern: %d %s", code, body)
	}
	// a key with no budget may use any model
	_, open := newKey(t, gw, `{"name":"open"}`)
	if code, _ := do(t, gw, "POST", "/v1/chat/completions", open, chat("fake/unlisted")); code != 200 {
		t.Fatal("key without budget was refused")
	}
}

func TestStreamedUsageCountsAndSurvivesRestart(t *testing.T) {
	db := filepath.Join(t.TempDir(), "t.db")
	gw, st, seen := spendStack(t, db)
	_, secret := newKey(t, gw, `{"name":"s","budget_usd":1}`)
	code, _ := do(t, gw, "POST", "/v1/chat/completions", secret, `{"model":"fake/m","stream":true,"messages":[{"role":"user","content":"hi"}]}`)
	if code != 200 || !strings.Contains(*seen, "include_usage") {
		t.Fatalf("stream: %d, upstream stream_options = %q", code, *seen)
	}
	id := keyIDOf(t, gw)
	if got := st.keys.spentThisMonth(id); got < 0.019 || got > 0.021 {
		t.Fatalf("streamed spend = %v, want 0.02", got)
	}
	st.flush()
	// A new process reads the month's spend back from the database.
	st2, err := openStore(db, 30)
	if err != nil {
		t.Fatal(err)
	}
	defer st2.close()
	if got := st2.keys.spentThisMonth(id); got < 0.019 || got > 0.021 {
		t.Fatalf("spend after restart = %v, want 0.02", got)
	}
}

func TestPriceLookup(t *testing.T) {
	prices = map[string]price{"a/b": {1, 2}, "a/*": {3, 4}, "a/b-long*": {5, 6}}
	defer func() { prices = nil }()
	for model, want := range map[string]price{"a/b": {1, 2}, "a/other": {3, 4}, "a/b-long-2": {5, 6}} {
		if p, ok := priceOf(model); !ok || p != want {
			t.Fatalf("%s = %v %v, want %v", model, p, ok, want)
		}
	}
	if _, ok := priceOf("z/y"); ok {
		t.Fatal("unlisted model has a price")
	}
	if c := (price{In: 0.15, Out: 0.6}).cost(1_000_000, 500_000); c != 0.45 {
		t.Fatalf("cost = %v", c)
	}
}
