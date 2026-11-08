// The admin side: a small web page and the JSON API behind it. Master key only.
//
//	GET  /admin                       the page (static, no data in it; it asks for the key)
//	GET  /admin/api/summary           counts by result and by key
//	GET  /admin/api/series            requests per time bucket, for the traffic chart
//	GET  /admin/api/events            recent requests, newest first, with ?q= ?result= ?key= ?threat=1
//	GET  /admin/api/keys              virtual keys
//	POST /admin/api/keys              create one; the secret is in the answer once
//	POST /admin/api/keys/{id}/revoke  stop a key now
//	GET  /admin/api/config            how the gateway is set up (read only, no secrets)
package main

import (
	"crypto/sha256"
	_ "embed"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"math"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"
)

//go:embed admin.html
var adminHTML string

// adminCSP allows exactly the page's own style and script blocks (by hash) and calls back to this server.
// Nothing else can run, so even a bug that put attacker text into the page could not run a script.
var adminCSP = func() string {
	hash := func(open, end string) string {
		i := strings.Index(adminHTML, open)
		j := strings.Index(adminHTML, end)
		if i < 0 || j < i {
			panic("admin.html needs one " + open + " block")
		}
		// Browsers read a line break in an inline block as LF whatever the file has, so hash it that way (a Windows checkout may have CRLF).
		sum := sha256.Sum256([]byte(strings.ReplaceAll(adminHTML[i+len(open):j], "\r\n", "\n")))
		return "'sha256-" + base64.StdEncoding.EncodeToString(sum[:]) + "'"
	}
	return "default-src 'none'; script-src " + hash("<script>", "</script>") + "; style-src " + hash("<style>", "</style>") +
		"; connect-src 'self'; base-uri 'none'; form-action 'none'; frame-ancestors 'none'"
}()

// serverInfo is what main knows about how the server was started, shown read-only on the settings tab.
type serverInfo struct {
	Listen      string
	MaxInflight int
	Hosts       []string
	DBPath      string
	KeepDays    int
	AccessLog   string
	Started     time.Time
}

var runtimeInfo serverInfo

type admin struct {
	st  *store
	ps  map[string]provider
	pol policy
}

func newAdmin(st *store, ps map[string]provider, pol policy) *admin { return &admin{st, ps, pol} }

func (a *admin) routes(mux *http.ServeMux) {
	mux.HandleFunc("GET /admin", a.page)
	mux.HandleFunc("GET /admin/{$}", a.page)
	mux.HandleFunc("GET /admin/api/summary", adminOnly(a.summary))
	mux.HandleFunc("GET /admin/api/series", adminOnly(a.series))
	mux.HandleFunc("GET /admin/api/events", adminOnly(a.events))
	mux.HandleFunc("GET /admin/api/keys", adminOnly(a.listKeys))
	mux.HandleFunc("POST /admin/api/keys", adminOnly(a.createKey))
	mux.HandleFunc("POST /admin/api/keys/{id}/revoke", adminOnly(a.revokeKey))
	mux.HandleFunc("GET /admin/api/config", adminOnly(a.config))
}

func (a *admin) page(w http.ResponseWriter, r *http.Request) {
	h := w.Header()
	h.Set("Content-Type", "text/html; charset=utf-8")
	h.Set("Content-Security-Policy", adminCSP)
	h.Set("Cache-Control", "no-store")
	h.Set("X-Content-Type-Options", "nosniff")
	h.Set("X-Frame-Options", "DENY")
	h.Set("Referrer-Policy", "no-referrer")
	io.WriteString(w, adminHTML)
}

// ok answers 503 when there is no database.
func (a *admin) ok(w http.ResponseWriter) bool {
	if a.st == nil {
		jsonError(w, http.StatusServiceUnavailable, "stats_disabled", "The gateway has no database. Check DB_PATH and the gateway log.")
		return false
	}
	return true
}

func (a *admin) fail(w http.ResponseWriter, err error) {
	log.Printf("admin: %v", err)
	jsonError(w, http.StatusInternalServerError, "admin_error", "Could not read or write the database.")
}

// sinceParam reads ?since= and answers 400 itself when it is wrong.
func (a *admin) sinceParam(w http.ResponseWriter, r *http.Request) (time.Duration, bool) {
	since, err := parseSince(r.URL.Query().Get("since"))
	if err != nil {
		jsonError(w, http.StatusBadRequest, "bad_since", err.Error())
		return 0, false
	}
	return since, true
}

// summary is everything the overview page needs in one call: this period and the one before it,
// first-byte timing, what the filter caught by rule, and use per key.
func (a *admin) summary(w http.ResponseWriter, r *http.Request) {
	if !a.ok(w) {
		return
	}
	since, ok := a.sinceParam(w, r)
	if !ok {
		return
	}
	now := time.Now()
	from := now.Add(-since)
	byResult, byKey, err := a.st.eventSummary(from)
	if err != nil {
		a.fail(w, err)
		return
	}
	cur, err := a.st.totals(from, now.Add(time.Hour))
	if err != nil {
		a.fail(w, err)
		return
	}
	prev, err := a.st.totals(from.Add(-since), from)
	if err != nil {
		a.fail(w, err)
		return
	}
	p50, p95, answered, err := a.st.firstByte(from)
	if err != nil {
		a.fail(w, err)
		return
	}
	rules, err := a.st.rules(from)
	if err != nil {
		a.fail(w, err)
		return
	}
	names := map[string]string{"master": "master key"}
	if keys, err := a.st.keys.list(); err == nil {
		for _, k := range keys {
			names[k.ID] = k.Name
		}
	}
	results := map[string]int{}
	var weighted float64
	for _, r := range byResult {
		results[r.Result] = r.Count
		weighted += r.AvgMS * float64(r.Count)
	}
	avg, rate := 0.0, 0.0
	if cur.Requests > 0 {
		avg = math.Round(weighted / float64(cur.Requests))
		rate = math.Round(float64(cur.ProviderErrors)/float64(cur.Requests)*1e4) / 1e4
	}
	keys := []map[string]any{}
	for _, k := range byKey {
		keys = append(keys, map[string]any{"key": k.Key, "name": names[k.Key], "requests": k.Count, "blocked": k.Blocked, "limited_or_denied": k.LimitedOrDenied, "last_seen": k.LastSeen, "cost_usd": k.CostUSD})
	}
	rs := []map[string]any{}
	for _, c := range rules {
		rs = append(rs, map[string]any{"filter": c.Filter, "rule": c.Rule, "count": c.Count, "blocked": c.Blocked})
	}
	pj := func(t totals) map[string]any {
		return map[string]any{"requests": t.Requests, "blocked": t.Blocked, "flagged": t.Flagged, "provider_errors": t.ProviderErrors, "refused": t.Refused, "cost_usd": t.CostUSD}
	}
	writeJSON(w, map[string]any{"since": since.String(), "requests": cur.Requests, "flagged": cur.Flagged, "blocked": cur.Blocked, "refused": cur.Refused,
		"provider_errors": cur.ProviderErrors, "provider_error_rate": rate, "avg_total_ms": avg, "first_byte_p50_ms": p50, "first_byte_p95_ms": p95,
		"answered": answered, "cost_usd": cur.CostUSD, "in_tokens": cur.InTok, "out_tokens": cur.OutTok, "priced": len(prices) > 0, "previous": pj(prev), "by_result": results, "by_key": keys, "by_rule": rs, "dropped": a.st.dropped.Load()})
}

// series is the traffic chart: counts in equal time buckets across the period.
func (a *admin) series(w http.ResponseWriter, r *http.Request) {
	if !a.ok(w) {
		return
	}
	since, ok := a.sinceParam(w, r)
	if !ok {
		return
	}
	n, _ := strconv.Atoi(r.URL.Query().Get("buckets"))
	if n < 2 || n > 240 {
		n = 60
	}
	step := int64(since.Seconds()) / int64(n)
	if step < 1 {
		step = 1
	}
	from := time.Now().Add(-time.Duration(step*int64(n)) * time.Second)
	bs, err := a.st.series(from, step, n)
	if err != nil {
		a.fail(w, err)
		return
	}
	out := make([]map[string]any, len(bs))
	for i, b := range bs {
		out[i] = map[string]any{"t": b.T, "total": b.Total, "ok": b.OK, "blocked": b.Blocked, "flagged": b.Flagged, "limited": b.Limited, "errors": b.Errors}
	}
	writeJSON(w, map[string]any{"start": from.Unix(), "step": step, "buckets": out})
}

func (a *admin) events(w http.ResponseWriter, r *http.Request) {
	if !a.ok(w) {
		return
	}
	since, ok := a.sinceParam(w, r)
	if !ok {
		return
	}
	q := r.URL.Query()
	limit, _ := strconv.Atoi(q.Get("limit"))
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	search := strings.TrimSpace(q.Get("q"))
	if len(search) > 100 {
		search = search[:100]
	}
	evs, err := a.st.listEvents(eventFilter{Since: time.Now().Add(-since), Result: q.Get("result"), Key: q.Get("key"), Q: search, Threat: q.Get("threat") == "1", Attention: q.Get("attention") == "1", Limit: limit})
	if err != nil {
		a.fail(w, err)
		return
	}
	writeJSON(w, map[string]any{"data": evs})
}

type keyJSON struct {
	ID      string   `json:"id"`
	Name    string   `json:"name"`
	Prefix  string   `json:"prefix"`
	Created int64    `json:"created"`
	Revoked bool     `json:"revoked"`
	Models  []string `json:"models"`
	RPM     int      `json:"rpm"`
	Budget  float64  `json:"budget_usd"`
	Spent   float64  `json:"spent_usd"`
}

func (a *admin) toJSON(k keyRec) keyJSON {
	m := k.Models
	if m == nil {
		m = []string{}
	}
	return keyJSON{k.ID, k.Name, k.Prefix, k.Created.Unix(), k.Revoked, m, k.RPM, k.BudgetUSD, math.Round(a.st.keys.spentThisMonth(k.ID)*1e6) / 1e6}
}

func (a *admin) listKeys(w http.ResponseWriter, r *http.Request) {
	if !a.ok(w) {
		return
	}
	recs, err := a.st.keys.list()
	if err != nil {
		a.fail(w, err)
		return
	}
	out := []keyJSON{}
	for _, k := range recs {
		out = append(out, a.toJSON(k))
	}
	writeJSON(w, map[string]any{"data": out})
}

func (a *admin) createKey(w http.ResponseWriter, r *http.Request) {
	if !a.ok(w) {
		return
	}
	var in struct {
		Name   string   `json:"name"`
		Models []string `json:"models"`
		RPM    int      `json:"rpm"`
		Budget float64  `json:"budget_usd"`
	}
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 64<<10))
	dec.DisallowUnknownFields() // a typo such as "budget" must not quietly create a key with no limit
	if err := dec.Decode(&in); err != nil {
		jsonError(w, http.StatusBadRequest, "bad_json", "Body must be JSON like {\"name\":\"my app\",\"models\":[\"groq/*\"],\"rpm\":60,\"budget_usd\":25} ("+err.Error()+").")
		return
	}
	rec, secret, err := a.st.keys.create(in.Name, in.Models, in.RPM, in.Budget)
	if err != nil {
		jsonError(w, http.StatusBadRequest, "bad_key", err.Error())
		return
	}
	log.Printf("admin: created key %s (%s)", rec.ID, rec.Name)
	w.WriteHeader(http.StatusCreated)
	writeJSON(w, map[string]any{"key": secret, "data": a.toJSON(rec), "note": "Copy the key now. It is not stored and cannot be shown again."})
}

func (a *admin) revokeKey(w http.ResponseWriter, r *http.Request) {
	if !a.ok(w) {
		return
	}
	ok, err := a.st.keys.revoke(r.PathValue("id"))
	if err != nil {
		a.fail(w, err)
		return
	}
	if !ok {
		jsonError(w, http.StatusNotFound, "no_such_key", "No active key with that id.")
		return
	}
	log.Printf("admin: revoked key %s", r.PathValue("id"))
	writeJSON(w, map[string]any{"revoked": r.PathValue("id")})
}

func (a *admin) config(w http.ResponseWriter, r *http.Request) {
	var fs []string
	for _, f := range a.pol.filters {
		fs = append(fs, f.name)
	}
	type prov struct {
		Name    string `json:"name"`
		BaseURL string `json:"base_url"`
		KeySet  bool   `json:"key_set"`
	}
	ps := []prov{}
	for _, n := range providerNames(a.ps) {
		ps = append(ps, prov{n, a.ps[n].base.Redacted(), a.ps[n].key != ""})
	}
	log := runtimeInfo.AccessLog
	if log == "" {
		log = "off"
	}
	sort.Strings(fs)
	writeJSON(w, map[string]any{
		"listen": runtimeInfo.Listen, "allowed_hosts": runtimeInfo.Hosts, "max_inflight": runtimeInfo.MaxInflight,
		"filters": fs, "filter_block": a.pol.block, "filter_log": a.pol.log, "scan_system_messages": a.pol.scanSystem,
		"scan_limit_bytes": a.pol.maxScan, "db_path": runtimeInfo.DBPath, "stats_days": runtimeInfo.KeepDays,
		"access_log": log, "providers": ps, "database": a.st != nil, "started": runtimeInfo.Started.Unix(),
		"note": fmt.Sprintf("Read only. Change these in .env and restart; editing from this page is on the roadmap."),
	})
}
