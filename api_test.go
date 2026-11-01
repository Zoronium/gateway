package main

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func testStore(t *testing.T) *store {
	st, err := openStore(filepath.Join(t.TempDir(), "t.db"), 30)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(st.close)
	return st
}

func TestClassify(t *testing.T) {
	for _, c := range []struct {
		status int
		err    error
		want   string
	}{
		{200, nil, "ok"}, {204, nil, "ok"}, {429, nil, "rate_limited"}, {500, nil, "server_error"}, {503, nil, "server_error"},
		{401, nil, "client_error"}, {404, nil, "client_error"}, {410, nil, "client_error"}, {0, io.EOF, "unreachable"},
	} {
		if got := classify(c.status, c.err); got != c.want {
			t.Errorf("classify(%d, %v) = %s, want %s", c.status, c.err, got, c.want)
		}
	}
}

func TestParseSince(t *testing.T) {
	for in, want := range map[string]time.Duration{"": 24 * time.Hour, "30m": 30 * time.Minute, "24h": 24 * time.Hour, "7d": 7 * 24 * time.Hour, "0.5d": 12 * time.Hour} {
		if got, err := parseSince(in); err != nil || got != want {
			t.Errorf("parseSince(%q) = %v, %v; want %v", in, got, err, want)
		}
	}
	for _, bad := range []string{"bogus", "-1h", "0", "0d", "400d", "d", "1x"} {
		if _, err := parseSince(bad); err == nil {
			t.Errorf("parseSince(%q) should fail", bad)
		}
	}
}

func TestStoreCountsErrorRates(t *testing.T) {
	st := testStore(t)
	now := time.Now()
	add := func(p, m, class string, status int, ms int64, age time.Duration) {
		st.record(attempt{now.Add(-age), p, m, status, class, ms})
	}
	for i := 0; i < 6; i++ {
		add("groq", "a", "ok", 200, 100, 0)
	}
	add("groq", "a", "rate_limited", 429, 20, 0)
	add("groq", "a", "stream_error", 200, 50, 0)
	add("groq", "b", "ok", 200, 300, 0)
	add("groq", "b", "ok", 200, 500, 0)
	add("nvidia", "gone", "client_error", 410, 10, 0)
	add("groq", "a", "server_error", 500, 10, 3*24*time.Hour) // outside a 24h window
	st.flush()

	rows, err := st.stats(now.Add(-24*time.Hour), true)
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]statRow{}
	for _, r := range rows {
		got[r.Provider+"/"+r.Model] = r
	}
	a := got["groq/a"]
	if a.Requests != 8 || a.errors() != 2 || a.ByClass["rate_limited"] != 1 || a.ByClass["stream_error"] != 1 || a.ByClass["server_error"] != 0 {
		t.Fatalf("groq/a = %+v", a)
	}
	if a.AvgTTFB == nil || *a.AvgTTFB != 100 { // averaged over successful tries only
		t.Fatalf("groq/a avg ttfb = %v, want 100", a.AvgTTFB)
	}
	if j := a.json(); j.ErrorRate != 0.25 {
		t.Fatalf("groq/a error rate = %v, want 0.25", j.ErrorRate)
	}
	if g := got["nvidia/gone"]; g.Requests != 1 || g.errors() != 1 || g.AvgTTFB != nil || g.json().ErrorRate != 1 {
		t.Fatalf("nvidia/gone = %+v", g)
	}
	byProvider, _ := st.stats(now.Add(-24*time.Hour), false)
	if len(byProvider) != 2 || byProvider[0].Provider != "groq" || byProvider[0].Requests != 10 || byProvider[0].errors() != 2 {
		t.Fatalf("by provider = %+v", byProvider)
	}
	wide, _ := st.stats(now.Add(-7*24*time.Hour), true)
	for _, r := range wide {
		if r.Provider == "groq" && r.Model == "a" && r.Requests != 9 {
			t.Fatalf("a 7d window should include the old attempt, got %d", r.Requests)
		}
	}
}

func TestStorePrunesOldAttempts(t *testing.T) {
	st := testStore(t)
	st.record(attempt{time.Now().AddDate(0, 0, -40), "groq", "old", 200, "ok", 1})
	st.record(attempt{time.Now(), "groq", "new", 200, "ok", 1})
	st.flush()
	st.prune(30)
	rows, _ := st.stats(time.Now().AddDate(0, 0, -100), true)
	if len(rows) != 1 || rows[0].Model != "new" {
		t.Fatalf("after prune: %+v", rows)
	}
}

func TestStoreKeepsOldModelsWhenARefreshFails(t *testing.T) {
	st := testStore(t)
	if err := st.saveModels("groq", []string{"b", "a", "a"}, time.Now()); err != nil {
		t.Fatal(err)
	}
	if err := st.saveRefreshError("groq", "HTTP 500", time.Now()); err != nil {
		t.Fatal(err)
	}
	models, _ := st.models()
	refs, _ := st.refreshes()
	if strings.Join(models["groq"], ",") != "a,b" || refs["groq"].OK || refs["groq"].Error != "HTTP 500" || refs["groq"].Count != 2 {
		t.Fatalf("models %v, refresh %+v", models, refs["groq"])
	}
}

// apiGateway runs the real handler with a fake provider that serves a model list and chat answers.
func apiGateway(t *testing.T, statusFor func(model string) int) (*httptest.Server, *store, *atomic.Bool) {
	listFails := &atomic.Bool{}
	fake := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == "GET" && strings.HasSuffix(r.URL.Path, "/models"):
			if listFails.Load() {
				http.Error(w, "boom", 500)
				return
			}
			w.Write([]byte(`{"object":"list","data":[{"id":"beta"},{"id":"alpha"},{"id":"alpha"},{"id":""}]}`))
		case strings.HasSuffix(r.URL.Path, "/chat/completions"):
			var b struct{ Model string }
			json.NewDecoder(r.Body).Decode(&b)
			if b.Model == "cut" { // promise more bytes than we send, then hang up
				w.Header().Set("Content-Length", "1000")
				w.Write([]byte("data: partial"))
				return
			}
			if s := statusFor(b.Model); s != 200 {
				http.Error(w, "nope", s)
				return
			}
			w.Write([]byte(`{"ok":true}`))
		}
	}))
	t.Cleanup(fake.Close)
	st := testStore(t)
	gw := httptest.NewServer(newHandler(map[string]provider{"fake": prov(t, fake, "secret-provider-key")}, policy{}, st))
	t.Cleanup(gw.Close)
	return gw, st, listFails
}

func getJSON(t *testing.T, url string) (int, map[string]any) {
	resp, err := http.Get(url)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	if strings.Contains(string(b), "secret-provider-key") {
		t.Fatalf("a provider key leaked from %s: %s", url, b)
	}
	var out map[string]any
	json.Unmarshal(b, &out)
	return resp.StatusCode, out
}

func entry(t *testing.T, list map[string]any, id string) map[string]any {
	for _, e := range list["data"].([]any) {
		if m := e.(map[string]any); m["id"] == id {
			return m
		}
	}
	t.Fatalf("%s not in %v", id, list["data"])
	return nil
}

func TestAttemptsAreRecordedAndServedWithErrorRates(t *testing.T) {
	gw, st, _ := apiGateway(t, func(m string) int {
		if m == "gone" {
			return 410
		}
		return 200
	})
	chat := func(model string) {
		resp, err := http.Post(gw.URL+"/v1/chat/completions", "application/json", strings.NewReader(`{"model":"`+model+`","messages":[{"role":"user","content":"hi"}]}`))
		if err != nil {
			t.Fatal(err)
		}
		io.Copy(io.Discard, resp.Body)
		resp.Body.Close()
	}
	chat("fake/alpha")
	chat("fake/alpha")
	chat("fake/gone")
	chat("fake/cut")
	st.flush()

	code, list := getJSON(t, gw.URL+"/v1/models")
	if code != 200 || list["object"] != "list" {
		t.Fatalf("status %d, body %v", code, list)
	}
	alpha := entry(t, list, "fake/alpha")
	s := alpha["stats"].(map[string]any)
	if alpha["listed"] != true || alpha["owned_by"] != "fake" || s["requests"] != 2.0 || s["error_rate"] != 0.0 {
		t.Fatalf("alpha = %v", alpha)
	}
	if beta := entry(t, list, "fake/beta"); beta["listed"] != true || beta["stats"] != nil {
		t.Fatalf("beta should be listed with no stats: %v", beta)
	}
	if n := len(list["data"].([]any)); n != 4 { // alpha, beta, plus gone and cut which the provider never listed
		t.Fatalf("expected 4 entries (the empty and duplicate ids dropped), got %d: %v", n, list["data"])
	}
	gone := entry(t, list, "fake/gone")
	if gone["listed"] != false || gone["stats"].(map[string]any)["error_rate"] != 1.0 {
		t.Fatalf("retired model should show as unlisted with 100%% errors: %v", gone)
	}
	cut := entry(t, list, "fake/cut")["stats"].(map[string]any)
	if cut["by_class"].(map[string]any)["stream_error"] != 1.0 {
		t.Fatalf("a dropped stream should count as a stream_error: %v", cut)
	}

	_, stats := getJSON(t, gw.URL+"/v1/stats?since=1h&provider=fake")
	if stats["since"] != "1h0m0s" || stats["dropped"] != 0.0 || len(stats["data"].([]any)) != 3 {
		t.Fatalf("stats = %v", stats)
	}
	_, provs := getJSON(t, gw.URL+"/v1/providers")
	p := provs["data"].([]any)[0].(map[string]any)
	ps := p["stats"].(map[string]any)
	if p["name"] != "fake" || p["models"] != 2.0 || p["refresh_ok"] != true || ps["requests"] != 4.0 || ps["errors"] != 2.0 || ps["error_rate"] != 0.5 {
		t.Fatalf("provider = %v", p)
	}
}

func TestFallbackWritesOneRowPerProviderTried(t *testing.T) {
	bad := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { http.Error(w, "slow down", 429) }))
	good := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.Write([]byte(`{"ok":true}`)) }))
	defer bad.Close()
	defer good.Close()
	st := testStore(t)
	gw := httptest.NewServer(newHandler(map[string]provider{"bad": prov(t, bad, "k"), "good": prov(t, good, "k")}, policy{}, st))
	defer gw.Close()
	resp, _ := http.Post(gw.URL+"/v1/chat/completions", "application/json", strings.NewReader(`{"model":"bad/m1,good/m2"}`))
	io.Copy(io.Discard, resp.Body)
	resp.Body.Close()
	st.flush()
	rows, _ := st.stats(time.Now().Add(-time.Hour), true)
	if len(rows) != 2 || rows[0].Provider != "bad" || rows[0].ByClass["rate_limited"] != 1 || rows[1].Provider != "good" || rows[1].ByClass["ok"] != 1 {
		t.Fatalf("rows = %+v", rows)
	}
}

func TestModelRefreshFailureKeepsTheOldList(t *testing.T) {
	gw, _, listFails := apiGateway(t, func(string) int { return 200 })
	if _, list := getJSON(t, gw.URL+"/v1/models"); len(list["data"].([]any)) != 2 {
		t.Fatalf("first fetch: %v", list)
	}
	listFails.Store(true)
	_, list := getJSON(t, gw.URL+"/v1/models?refresh=1")
	if len(list["data"].([]any)) != 2 {
		t.Fatalf("old list should survive a failed refresh: %v", list)
	}
	_, provs := getJSON(t, gw.URL+"/v1/providers")
	p := provs["data"].([]any)[0].(map[string]any)
	if p["refresh_ok"] != false || p["refresh_error"] != "HTTP 500" || p["models"] != 2.0 {
		t.Fatalf("provider = %v", p)
	}
}

func TestAPIErrors(t *testing.T) {
	gw, _, _ := apiGateway(t, func(string) int { return 200 })
	for url, want := range map[string]int{
		"/v1/models?since=bogus":      400,
		"/v1/stats?since=400d":        400,
		"/v1/stats?provider=nope":     404,
		"/v1/providers?provider=nope": 404,
		"/v1/models?provider=fake":    200,
		"/v1/stats?since=7d":          200,
	} {
		if code, _ := getJSON(t, gw.URL+url); code != want {
			t.Errorf("%s: status %d, want %d", url, code, want)
		}
	}
	// No database: the proxy still works, the stats endpoints say so.
	off := httptest.NewServer(newHandler(map[string]provider{}, policy{}, nil))
	defer off.Close()
	for _, p := range []string{"/v1/models", "/v1/providers", "/v1/stats"} {
		if code, _ := getJSON(t, off.URL+p); code != 503 {
			t.Errorf("%s without a store: status %d, want 503", p, code)
		}
	}
}

func TestStatsEndpointsNeedTheGatewayKey(t *testing.T) {
	st := testStore(t)
	g := guard{key: testKey, hosts: map[string]bool{"127.0.0.1": true}, inflight: make(chan struct{}, 4)}
	gw := httptest.NewServer(g.wrap(newHandler(map[string]provider{}, policy{}, st)))
	defer gw.Close()
	for _, p := range []string{"/v1/models", "/v1/providers", "/v1/stats"} {
		if code, _ := getJSON(t, gw.URL+p); code != 401 {
			t.Errorf("%s without a key: status %d, want 401", p, code)
		}
		req, _ := http.NewRequest("GET", gw.URL+p, nil)
		req.Header.Set("Authorization", "Bearer "+testKey)
		resp, err := http.DefaultClient.Do(req)
		if err != nil || resp.StatusCode != 200 {
			t.Errorf("%s with a key: %v %v", p, resp, err)
		}
	}
}
