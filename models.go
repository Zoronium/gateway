// The read-only API: which models each provider offers, and how often each one fails.
//
//	GET /v1/models     OpenAI-style model list across all providers, with error rates
//	GET /v1/providers  one row per provider: model count, last refresh, error rate
//	GET /v1/stats      per provider and model: counts by outcome
//
// All three take ?since=24h (or 30m, 7d) and ?provider=groq. /v1/models also takes ?refresh=1.
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"math"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

type api struct {
	ps     map[string]provider
	st     *store
	client *http.Client
}

func newAPI(ps map[string]provider, st *store, client *http.Client) *api {
	return &api{ps: ps, st: st, client: client}
}

func providerNames(ps map[string]provider) []string {
	var n []string
	for k := range ps {
		n = append(n, k)
	}
	sort.Strings(n)
	return n
}

// fetch asks one provider for its model list. Errors are short on purpose: they are shown to clients.
func (a *api) fetch(ctx context.Context, name string, p provider) ([]string, error) {
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, "GET", p.base.JoinPath("models").String(), nil)
	if err != nil {
		return nil, fmt.Errorf("bad base URL")
	}
	if p.key != "" {
		req.Header.Set("Authorization", "Bearer "+p.key)
		if name == "anthropic" { // Anthropic's own list wants these headers
			req.Header.Set("x-api-key", p.key)
			req.Header.Set("anthropic-version", "2023-06-01")
		}
	}
	resp, err := a.client.Do(req)
	if err != nil {
		log.Printf("model list for %s: %v", name, err)
		return nil, fmt.Errorf("unreachable or timed out")
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	var out struct {
		Data []struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	if json.NewDecoder(io.LimitReader(resp.Body, 8<<20)).Decode(&out) != nil {
		return nil, fmt.Errorf("unreadable model list")
	}
	seen := map[string]bool{}
	var ids []string
	for _, m := range out.Data {
		if m.ID != "" && !seen[m.ID] {
			seen[m.ID] = true
			ids = append(ids, m.ID)
		}
	}
	sort.Strings(ids)
	return ids, nil
}

// refresh updates the named providers at the same time. A failure keeps the old list.
func (a *api) refresh(ctx context.Context, names []string) {
	var wg sync.WaitGroup
	for _, name := range names {
		p := a.ps[name]
		wg.Add(1)
		go func() {
			defer wg.Done()
			ids, err := a.fetch(ctx, name, p)
			if err != nil {
				err = a.st.saveRefreshError(name, err.Error(), time.Now())
			} else {
				err = a.st.saveModels(name, ids, time.Now())
			}
			if err != nil {
				log.Printf("saving model list for %s: %v", name, err)
			}
		}()
	}
	wg.Wait()
}

// startRefresher fetches every provider's model list at startup and then every interval.
func startRefresher(ps map[string]provider, st *store, every time.Duration) {
	if st == nil {
		return
	}
	a := newAPI(ps, st, &http.Client{})
	go func() {
		for {
			a.refresh(context.Background(), providerNames(ps))
			time.Sleep(every)
		}
	}()
}

// parseSince reads 30m, 24h or 7d. The default is 24h.
func parseSince(s string) (time.Duration, error) {
	if s == "" {
		return 24 * time.Hour, nil
	}
	var d time.Duration
	var err error
	if n, ok := strings.CutSuffix(s, "d"); ok {
		var days float64
		days, err = strconv.ParseFloat(n, 64)
		d = time.Duration(days * 24 * float64(time.Hour))
	} else {
		d, err = time.ParseDuration(s)
	}
	if err != nil || d <= 0 || d > 366*24*time.Hour {
		return 0, fmt.Errorf("since must look like 30m, 24h or 7d, and be at most 366d")
	}
	return d, nil
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(v)
}

// statJSON is the error summary shown for a model or a provider.
type statJSON struct {
	Requests  int            `json:"requests"`
	Errors    int            `json:"errors"`
	ErrorRate float64        `json:"error_rate"`
	AvgTTFBMS *float64       `json:"avg_ttfb_ms"`
	ByClass   map[string]int `json:"by_class"`
}

func (r statRow) json() *statJSON {
	rate := float64(r.errors()) / float64(r.Requests)
	var ttfb *float64
	if r.AvgTTFB != nil {
		v := math.Round(*r.AvgTTFB)
		ttfb = &v
	}
	return &statJSON{r.Requests, r.errors(), math.Round(rate*1e4) / 1e4, ttfb, r.ByClass}
}

// ready answers 503 when there is no database, and reads the shared ?since and ?provider options.
func (a *api) ready(w http.ResponseWriter, r *http.Request) (since time.Duration, only string, ok bool) {
	if a.st == nil {
		jsonError(w, http.StatusServiceUnavailable, "stats_disabled", "The gateway has no database, so there are no stats. Check DB_PATH and the gateway log.")
		return
	}
	since, err := parseSince(r.URL.Query().Get("since"))
	if err != nil {
		jsonError(w, http.StatusBadRequest, "bad_since", err.Error())
		return
	}
	only = r.URL.Query().Get("provider")
	if _, found := a.ps[only]; only != "" && !found {
		jsonError(w, http.StatusNotFound, "unknown_provider", fmt.Sprintf("No provider %q. Configured: %s", only, names(a.ps)))
		return
	}
	return since, only, true
}

func (a *api) fail(w http.ResponseWriter, err error) {
	log.Printf("stats query failed: %v", err)
	jsonError(w, http.StatusInternalServerError, "stats_error", "Could not read the stats database.")
}

type modelEntry struct {
	ID       string    `json:"id"`
	Object   string    `json:"object"`
	Created  int64     `json:"created"`
	OwnedBy  string    `json:"owned_by"`
	Provider string    `json:"provider"`
	Model    string    `json:"model"`
	Listed   bool      `json:"listed"` // false: seen in requests but not in the provider's own list, such as a retired model
	Stats    *statJSON `json:"stats,omitempty"`
}

func (a *api) listModels(w http.ResponseWriter, r *http.Request) {
	since, only, ok := a.ready(w, r)
	if !ok {
		return
	}
	refs, err := a.st.refreshes()
	if err != nil {
		a.fail(w, err)
		return
	}
	var todo []string
	for _, name := range providerNames(a.ps) {
		if (only == "" || only == name) && (r.URL.Query().Get("refresh") == "1" || refs[name].TS == 0) {
			todo = append(todo, name)
		}
	}
	if len(todo) > 0 {
		a.refresh(r.Context(), todo)
		if refs, err = a.st.refreshes(); err != nil {
			a.fail(w, err)
			return
		}
	}
	listed, err := a.st.models()
	if err != nil {
		a.fail(w, err)
		return
	}
	rows, err := a.st.stats(time.Now().Add(-since), true)
	if err != nil {
		a.fail(w, err)
		return
	}
	byKey := map[string]statRow{}
	for _, row := range rows {
		byKey[row.Provider+"\x00"+row.Model] = row
	}
	data := []modelEntry{}
	for _, name := range providerNames(a.ps) {
		if only != "" && only != name {
			continue
		}
		have := map[string]bool{}
		add := func(id string, isListed bool) {
			e := modelEntry{ID: name + "/" + id, Object: "model", Created: refs[name].TS, OwnedBy: name, Provider: name, Model: id, Listed: isListed}
			if row, found := byKey[name+"\x00"+id]; found {
				e.Stats = row.json()
			}
			data = append(data, e)
		}
		for _, id := range listed[name] {
			have[id] = true
			add(id, true)
		}
		for _, row := range rows {
			if row.Provider == name && !have[row.Model] {
				add(row.Model, false)
			}
		}
	}
	writeJSON(w, map[string]any{"object": "list", "since": since.String(), "data": data})
}

type providerEntry struct {
	Name         string    `json:"name"`
	BaseURL      string    `json:"base_url"`
	Models       int       `json:"models"`
	Refreshed    *int64    `json:"refreshed"`
	RefreshOK    *bool     `json:"refresh_ok"`
	RefreshError string    `json:"refresh_error,omitempty"`
	Stats        *statJSON `json:"stats,omitempty"`
}

func (a *api) listProviders(w http.ResponseWriter, r *http.Request) {
	since, only, ok := a.ready(w, r)
	if !ok {
		return
	}
	refs, err := a.st.refreshes()
	if err != nil {
		a.fail(w, err)
		return
	}
	rows, err := a.st.stats(time.Now().Add(-since), false)
	if err != nil {
		a.fail(w, err)
		return
	}
	byProvider := map[string]statRow{}
	for _, row := range rows {
		byProvider[row.Provider] = row
	}
	data := []providerEntry{}
	for _, name := range providerNames(a.ps) {
		if only != "" && only != name {
			continue
		}
		e := providerEntry{Name: name, BaseURL: a.ps[name].base.Redacted()}
		if ref, found := refs[name]; found {
			ts, ok := ref.TS, ref.OK
			e.Refreshed, e.RefreshOK, e.RefreshError, e.Models = &ts, &ok, ref.Error, ref.Count
		}
		if row, found := byProvider[name]; found {
			e.Stats = row.json()
		}
		data = append(data, e)
	}
	writeJSON(w, map[string]any{"object": "list", "since": since.String(), "data": data})
}

type statEntry struct {
	Provider string `json:"provider"`
	Model    string `json:"model"`
	*statJSON
}

func (a *api) getStats(w http.ResponseWriter, r *http.Request) {
	since, only, ok := a.ready(w, r)
	if !ok {
		return
	}
	rows, err := a.st.stats(time.Now().Add(-since), true)
	if err != nil {
		a.fail(w, err)
		return
	}
	data := []statEntry{}
	for _, row := range rows {
		if only == "" || only == row.Provider {
			data = append(data, statEntry{row.Provider, row.Model, row.json()})
		}
	}
	writeJSON(w, map[string]any{"object": "list", "since": since.String(), "dropped": a.st.dropped.Load(), "data": data})
}
