package main

import (
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"testing"
	"time"
)

// Filter cost alone: go test -run '^$' -bench Tokens -benchmem
func BenchmarkTokens(b *testing.B) {
	for _, size := range []int{1 << 10, 10 << 10, 100 << 10} {
		text := strings.Repeat("the quick brown fox jumps over the lazy dog. ", size/45+1)[:size]
		b.Run(fmt.Sprintf("%dKB", size>>10), func(b *testing.B) {
			b.SetBytes(int64(size))
			for i := 0; i < b.N; i++ {
				tokensScore(text)
			}
		})
	}
}

// realTexts loads real documents and code, because synthetic text avoids the common words
// that send real text down the slow path. Files that are missing are skipped.
func realTexts() map[string]string {
	read := func(paths ...string) string {
		var sb strings.Builder
		for _, p := range paths {
			if b, err := os.ReadFile(p); err == nil {
				sb.Write(b)
			}
		}
		return sb.String()
	}
	goroot := os.Getenv("GOROOT")
	if goroot == "" {
		goroot = runtime.GOROOT()
	}
	httpDir := filepath.Join(goroot, "src", "net", "http")
	var stdlib []string
	if files, err := filepath.Glob(filepath.Join(httpDir, "*.go")); err == nil {
		for _, f := range files {
			if !strings.HasSuffix(f, "_test.go") {
				stdlib = append(stdlib, f)
			}
		}
	}
	out := map[string]string{
		"project docs":       read("../RESULTS.md", "../REVIEW.md", "../BUILD-CARD.md"),
		"project code":       read("main.go", "filters.go"),
		"stdlib server.go":   read(filepath.Join(httpDir, "server.go")),
		"stdlib net/http":    read(stdlib...),
		"stdlib one big doc": read(filepath.Join(httpDir, "doc.go"), filepath.Join(httpDir, "request.go")),
	}
	for k, v := range out {
		if v == "" {
			delete(out, k)
		}
	}
	return out
}

// Filter cost on real text: go test -run '^$' -bench TokensReal -benchmem
func BenchmarkTokensReal(b *testing.B) {
	texts := realTexts()
	var names []string
	for n := range texts {
		names = append(names, n)
	}
	sort.Strings(names)
	for _, name := range names {
		text := texts[name]
		b.Run(fmt.Sprintf("%s/%dKB", strings.ReplaceAll(name, " ", "_"), len(text)>>10), func(b *testing.B) {
			b.SetBytes(int64(len(text)))
			for i := 0; i < b.N; i++ {
				tokensScore(text)
			}
		})
	}
}

// Latency the gateway adds on top of a mock provider, one request at a time.
// Run: BENCH=1 go test -run TestOverhead -v     (PowerShell: $env:BENCH=1)
func TestOverhead(t *testing.T) {
	if os.Getenv("BENCH") == "" {
		t.Skip("set BENCH=1 to run")
	}
	fake := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.Copy(io.Discard, r.Body)
		w.Write([]byte(`{"id":"x","choices":[{"message":{"role":"assistant","content":"hi"}}]}`))
	}))
	defer fake.Close()
	ps := map[string]provider{"fake": prov(t, fake, "k")}
	none := httptest.NewServer(newHandler(ps, policy{}, nil))
	defer none.Close()
	tokens := httptest.NewServer(newHandler(ps, defaultPolicy(), nil))
	defer tokens.Close()

	client := &http.Client{Transport: &http.Transport{MaxIdleConnsPerHost: 4}}
	means := map[string]time.Duration{}
	measure := func(url string, body string, n int) []time.Duration {
		do := func() time.Duration {
			start := time.Now()
			resp, err := client.Post(url, "application/json", strings.NewReader(body))
			if err != nil {
				t.Fatal(err)
			}
			io.Copy(io.Discard, resp.Body)
			resp.Body.Close()
			return time.Since(start)
		}
		for i := 0; i < 300; i++ { // warm up connections and caches
			do()
		}
		d := make([]time.Duration, n)
		begin := time.Now()
		for i := range d {
			d[i] = do()
		}
		// The per-request clock is coarse on Windows (about 1 ms steps), so the mean comes from the total.
		means[url+body] = time.Since(begin) / time.Duration(n)
		sort.Slice(d, func(i, j int) bool { return d[i] < d[j] })
		return d
	}
	at := func(d []time.Duration, p float64) time.Duration { return d[int(float64(len(d)-1)*p)] }
	us := func(d time.Duration) string { return fmt.Sprintf("%.0f", float64(d.Nanoseconds())/1000) }

	fmt.Printf("\n%s/%s, %d CPUs, %s, 1 request at a time, mock provider on loopback\n", runtime.GOOS, runtime.GOARCH, runtime.NumCPU(), runtime.Version())
	fmt.Printf("\n| Body | Path | Mean µs | p99 µs | Added mean µs | Added p99 µs |\n|---|---|---|---|---|---|\n")
	for _, c := range []struct {
		name string
		text string
		n    int
	}{
		{"1 KB", strings.Repeat("Summarise the meeting notes for me please. ", 24), 5000},
		{"50 KB", strings.Repeat("Summarise the meeting notes for me please. ", 1200), 2000},
	} {
		body := `{"model":"fake/m","messages":[{"role":"user","content":"` + c.text + `"}]}`
		directURL := fake.URL + "/v1/chat/completions"
		direct := measure(directURL, body, c.n)
		for _, row := range []struct{ name, url string }{
			{"direct to provider", directURL},
			{"gateway, no filters", none.URL + "/v1/chat/completions"},
			{"gateway, token filter", tokens.URL + "/v1/chat/completions"},
		} {
			d := direct
			if row.url != directURL {
				d = measure(row.url, body, c.n)
			}
			addedMean, added99 := "-", "-"
			if row.url != directURL {
				addedMean, added99 = us(means[row.url+body]-means[directURL+body]), us(at(d, .99)-at(direct, .99))
			}
			fmt.Printf("| %s | %s | %s | %s | %s | %s |\n", c.name, row.name, us(means[row.url+body]), us(at(d, .99)), addedMean, added99)
		}
	}
}
