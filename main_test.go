package main

import (
	"bufio"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func prov(t *testing.T, s *httptest.Server, key string) provider {
	u, err := url.Parse(s.URL + "/v1")
	if err != nil {
		t.Fatal(err)
	}
	return provider{u, key}
}

func post(t *testing.T, gw *httptest.Server, body string) *http.Response {
	req, _ := http.NewRequest("POST", gw.URL+"/v1/chat/completions", strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer client-key")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	return resp
}

func TestStreamsStripsPrefixAndSwapsKey(t *testing.T) {
	release := make(chan struct{})
	type seen struct{ auth, model string }
	got := make(chan seen, 1)
	fake := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var b struct{ Model string }
		json.NewDecoder(r.Body).Decode(&b)
		got <- seen{r.Header.Get("Authorization"), b.Model}
		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("Set-Cookie", "session=provider-secret")
		w.Write([]byte("data: one\n\n"))
		w.(http.Flusher).Flush()
		<-release // hold the second chunk until the client has seen the first
		w.Write([]byte("data: two\n\n"))
	}))
	defer fake.Close()
	gw := httptest.NewServer(newHandler(map[string]provider{"fake": prov(t, fake, "provider-key")}, policy{}, nil))
	defer gw.Close()

	resp := post(t, gw, `{"model":"fake/org/model-x","stream":true}`)
	defer resp.Body.Close()
	first := make(chan string, 1)
	go func() {
		line, _ := bufio.NewReader(resp.Body).ReadString('\n')
		first <- line
	}()
	select {
	case line := <-first:
		if line != "data: one\n" {
			t.Fatalf("first chunk = %q", line)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("first chunk not streamed before upstream finished")
	}
	close(release)

	s := <-got
	if s.auth != "Bearer provider-key" || s.model != "org/model-x" {
		t.Fatalf("upstream saw %+v", s)
	}
	if h := resp.Header.Get("X-Gateway-Provider"); h != "fake/org/model-x" {
		t.Fatalf("X-Gateway-Provider = %q", h)
	}
	if c := resp.Header.Get("Set-Cookie"); c != "" {
		t.Fatalf("provider cookie leaked to client: %q", c)
	}
}

func TestFallsBackOn429(t *testing.T) {
	badHits := 0
	bad := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		badHits++
		http.Error(w, "slow down", 429)
	}))
	defer bad.Close()
	good := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"ok":true}`))
	}))
	defer good.Close()
	gw := httptest.NewServer(newHandler(map[string]provider{"bad": prov(t, bad, "k"), "good": prov(t, good, "k")}, policy{}, nil))
	defer gw.Close()

	resp := post(t, gw, `{"model":"bad/m1,good/m2"}`)
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != 200 || string(b) != `{"ok":true}` {
		t.Fatalf("got %d %s", resp.StatusCode, b)
	}
	if h := resp.Header.Get("X-Gateway-Provider"); h != "good/m2" || badHits != 1 {
		t.Fatalf("served by %q, bad hit %d times", h, badHits)
	}
}

func TestLastFailurePassesThrough(t *testing.T) {
	bad := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "slow down", 429)
	}))
	defer bad.Close()
	gw := httptest.NewServer(newHandler(map[string]provider{"bad": prov(t, bad, "k")}, policy{}, nil))
	defer gw.Close()
	resp := post(t, gw, `{"model":"bad/m1"}`)
	if resp.StatusCode != 429 {
		t.Fatalf("status = %d, want 429", resp.StatusCode)
	}
}

func TestDotenv(t *testing.T) {
	path := filepath.Join(t.TempDir(), ".env")
	file := "# comment\n\nT_PLAIN=abc\nexport T_EXPORT = \"quoted value\"\nT_SINGLE='x'\nT_KEEP=fromfile\nnot a pair\n"
	if err := os.WriteFile(path, []byte(file), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("T_KEEP", "fromenv")
	for _, k := range []string{"T_PLAIN", "T_EXPORT", "T_SINGLE"} {
		t.Cleanup(func() { os.Unsetenv(k) })
	}
	loadDotenv(path)
	want := map[string]string{"T_PLAIN": "abc", "T_EXPORT": "quoted value", "T_SINGLE": "x", "T_KEEP": "fromenv"}
	for k, v := range want {
		if got := os.Getenv(k); got != v {
			t.Fatalf("%s = %q, want %q", k, got, v)
		}
	}
}

func TestBadRequestsAndRoutes(t *testing.T) {
	fake := httptest.NewServer(http.NotFoundHandler())
	defer fake.Close()
	gw := httptest.NewServer(newHandler(map[string]provider{"fake": prov(t, fake, "k")}, policy{}, nil))
	defer gw.Close()

	for _, body := range []string{`{"model":"nope/m"}`, `{"model":"fake"}`, `{}`, `not json`} {
		resp := post(t, gw, body)
		if resp.StatusCode != 400 {
			t.Fatalf("%s: status = %d, want 400", body, resp.StatusCode)
		}
	}
	if resp, _ := http.Get(gw.URL + "/admin"); resp.StatusCode != 404 {
		t.Fatalf("/admin status = %d, want 404", resp.StatusCode)
	}
	if resp, _ := http.Get(gw.URL + "/v1/chat/completions"); resp.StatusCode != 405 {
		t.Fatalf("GET status = %d, want 405", resp.StatusCode)
	}
}
