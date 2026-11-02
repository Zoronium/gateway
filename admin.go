// The admin side: a small web page and the JSON API behind it. Master key only.
//
//	GET  /admin                       the page (static, no data in it; it asks for the key)
//	GET  /admin/api/summary           counts by result and by key
//	GET  /admin/api/events            recent requests, newest first
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
		sum := sha256.Sum256([]byte(adminHTML[i+len(open) : j]))
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

func (a *admin) summary(w http.ResponseWriter, r *http.Request) {
	if !a.ok(w) {
		return
	}
	since, err := parseSince(r.URL.Query().Get("since"))
	if err != nil {
		jsonError(w, http.StatusBadRequest, "bad_since", err.Error())
		return
	}
	byResult, byKey, err := a.st.eventSummary(time.Now().Add(-since))
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
	total, flagged, provErr := 0, 0, 0
	var weighted float64
	results := map[string]int{}
	for _, r := range byResult {
		total += r.Count
		flagged += r.Flagged
		results[r.Result] = r.Count
		weighted += r.AvgMS * float64(r.Count)
		if r.Result == "provider_error" {
			provErr = r.Count
		}
	}
	avg, rate := 0.0, 0.0
	if total > 0 {
		avg = math.Round(weighted / float64(total))
		rate = math.Round(float64(provErr)/float64(total)*1e4) / 1e4
	}
	keys := []map[string]any{}
	for _, k := range byKey {
		keys = append(keys, map[string]any{"key": k.Key, "name": names[k.Key], "requests": k.Count, "blocked": k.Blocked, "limited_or_denied": k.LimitedOrDenied})
	}
	writeJSON(w, map[string]any{"since": since.String(), "requests": total, "flagged": flagged, "provider_error_rate": rate,
		"avg_total_ms": avg, "by_result": results, "by_key": keys, "dropped": a.st.dropped.Load()})
}

func (a *admin) events(w http.ResponseWriter, r *http.Request) {
	if !a.ok(w) {
		return
	}
	q := r.URL.Query()
	since, err := parseSince(q.Get("since"))
	if err != nil {
		jsonError(w, http.StatusBadRequest, "bad_since", err.Error())
		return
	}
	limit, _ := strconv.Atoi(q.Get("limit"))
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	evs, err := a.st.listEvents(time.Now().Add(-since), q.Get("result"), q.Get("key"), limit)
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
}

func toJSON(k keyRec) keyJSON {
	m := k.Models
	if m == nil {
		m = []string{}
	}
	return keyJSON{k.ID, k.Name, k.Prefix, k.Created.Unix(), k.Revoked, m, k.RPM}
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
		out = append(out, toJSON(k))
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
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 64<<10)).Decode(&in); err != nil {
		jsonError(w, http.StatusBadRequest, "bad_json", "Body must be JSON like {\"name\":\"my app\",\"models\":[\"groq/*\"],\"rpm\":60}.")
		return
	}
	rec, secret, err := a.st.keys.create(in.Name, in.Models, in.RPM)
	if err != nil {
		jsonError(w, http.StatusBadRequest, "bad_key", err.Error())
		return
	}
	log.Printf("admin: created key %s (%s)", rec.ID, rec.Name)
	w.WriteHeader(http.StatusCreated)
	writeJSON(w, map[string]any{"key": secret, "data": toJSON(rec), "note": "Copy the key now. It is not stored and cannot be shown again."})
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
		"access_log": log, "providers": ps, "database": a.st != nil,
		"note": fmt.Sprintf("Read only. Change these in .env and restart; editing from this page is on the roadmap."),
	})
}
