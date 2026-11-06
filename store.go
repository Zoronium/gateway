// Stats and model lists, kept in SQLite. This is the only file that touches the database.
// What is stored: provider, model, HTTP status, outcome class, time to first byte and a timestamp.
// Never prompts, answers, keys or client details.
package main

import (
	"database/sql"
	"fmt"
	"log"
	"sort"
	"strings"
	"sync/atomic"
	"time"

	_ "modernc.org/sqlite" // pure Go, so no C compiler is needed
)

const schema = `
CREATE TABLE IF NOT EXISTS attempts (
	ts INTEGER NOT NULL, provider TEXT NOT NULL, model TEXT NOT NULL,
	status INTEGER NOT NULL, class TEXT NOT NULL, latency_ms INTEGER NOT NULL);
CREATE INDEX IF NOT EXISTS attempts_ts ON attempts(ts);
CREATE INDEX IF NOT EXISTS attempts_pm ON attempts(provider, model, ts);
CREATE TABLE IF NOT EXISTS models (provider TEXT NOT NULL, id TEXT NOT NULL, PRIMARY KEY (provider, id));
CREATE TABLE IF NOT EXISTS refreshes (
	provider TEXT PRIMARY KEY, ts INTEGER NOT NULL, ok INTEGER NOT NULL,
	error TEXT NOT NULL DEFAULT '', count INTEGER NOT NULL DEFAULT 0);
CREATE TABLE IF NOT EXISTS events (
	ts INTEGER NOT NULL, req_id TEXT NOT NULL, key_id TEXT NOT NULL, model TEXT NOT NULL, provider TEXT NOT NULL,
	status INTEGER NOT NULL, result TEXT NOT NULL, flagged INTEGER NOT NULL, filter TEXT NOT NULL, rule TEXT NOT NULL,
	score REAL NOT NULL, ttfb_ms INTEGER NOT NULL, total_ms INTEGER NOT NULL);
CREATE INDEX IF NOT EXISTS events_ts ON events(ts);
CREATE TABLE IF NOT EXISTS keys (
	id TEXT PRIMARY KEY, name TEXT NOT NULL, hash TEXT NOT NULL UNIQUE, prefix TEXT NOT NULL,
	created INTEGER NOT NULL, revoked INTEGER NOT NULL DEFAULT 0, models TEXT NOT NULL DEFAULT '', rpm INTEGER NOT NULL DEFAULT 0);`

// Columns added after the first release. Each is added once; an old database is upgraded in place.
var migrations = []string{
	`ALTER TABLE events ADD COLUMN in_tok INTEGER NOT NULL DEFAULT 0`,
	`ALTER TABLE events ADD COLUMN out_tok INTEGER NOT NULL DEFAULT 0`,
	`ALTER TABLE events ADD COLUMN cost_u INTEGER NOT NULL DEFAULT 0`, // millionths of a US dollar
	`ALTER TABLE keys ADD COLUMN budget REAL NOT NULL DEFAULT 0`,
}

// One row per try at a provider. A fallback chain makes one row per provider it tried.
type attempt struct {
	ts              time.Time
	provider, model string
	status          int    // HTTP status, 0 when there was no response
	class           string // ok, rate_limited, server_error, unreachable, client_error, stream_error
	latencyMS       int64  // time until the provider's response headers arrived
}

// classify turns a result into an outcome class. Anything but "ok" counts as an error.
func classify(status int, err error) string {
	switch {
	case err != nil:
		return "unreachable"
	case status >= 200 && status < 300:
		return "ok"
	case status == 429:
		return "rate_limited"
	case status >= 500:
		return "server_error"
	default:
		return "client_error" // 400, 401, 403, 404, 410 and so on: a bad key, a retired model, a bad request
	}
}

type store struct {
	db      *sql.DB
	keys    *keyring // virtual keys, cached in memory
	evc     chan event
	in      chan attempt
	flushc  chan chan struct{}
	quit    chan struct{}
	done    chan struct{}
	dropped atomic.Int64
}

func openStore(path string, keepDays int) (*store, error) {
	db, err := sql.Open("sqlite", path+"?_pragma=journal_mode(WAL)&_pragma=busy_timeout(5000)&_pragma=synchronous(NORMAL)")
	if err != nil {
		return nil, err
	}
	// ponytail: one connection, so reads and writes take turns. Fine for a few requests a second;
	// move to a read pool or Postgres if the stats queries ever slow the writer.
	db.SetMaxOpenConns(1)
	if _, err := db.Exec(schema); err != nil {
		db.Close()
		return nil, fmt.Errorf("open %s: %w", path, err)
	}
	for _, m := range migrations {
		if _, err := db.Exec(m); err != nil && !strings.Contains(err.Error(), "duplicate column") {
			db.Close()
			return nil, fmt.Errorf("upgrade %s: %w", path, err)
		}
	}
	s := &store{db: db, in: make(chan attempt, 1024), evc: make(chan event, 1024), flushc: make(chan chan struct{}), quit: make(chan struct{}), done: make(chan struct{})}
	s.keys = newKeyring(s)
	if err := s.keys.load(); err != nil {
		db.Close()
		return nil, fmt.Errorf("load keys: %w", err)
	}
	if spent, err := s.spentSince(monthStart(time.Now())); err == nil {
		s.keys.spent = spent
	}
	s.prune(keepDays)
	go s.run(keepDays)
	return s, nil
}

// record queues an attempt without ever blocking a request. If the queue is full the attempt is
// dropped and counted, because a slow disk must not slow the proxy. Safe on a nil store.
func (s *store) record(a attempt) {
	if s == nil {
		return
	}
	if len(a.model) > 200 {
		a.model = a.model[:200] // a client can send any model name, so keep rows small
	}
	select {
	case s.in <- a:
	default:
		s.dropped.Add(1)
	}
}

// recordEvent queues one request event the same way record queues an attempt. Safe on a nil store.
func (s *store) recordEvent(e event) {
	if s == nil {
		return
	}
	select {
	case s.evc <- e:
	default:
		s.dropped.Add(1)
	}
}

// run writes queued attempts and events in batches, once a second or every 200 rows.
func (s *store) run(keepDays int) {
	defer close(s.done)
	tick, pruneTick := time.NewTicker(time.Second), time.NewTicker(time.Hour)
	defer tick.Stop()
	defer pruneTick.Stop()
	var batch []attempt
	var evs []event
	write := func() {
		if len(batch) > 0 {
			if err := s.insert(batch); err != nil {
				log.Printf("stats: %d attempts lost: %v", len(batch), err)
			}
			batch = batch[:0]
		}
		if len(evs) > 0 {
			if err := s.insertEvents(evs); err != nil {
				log.Printf("stats: %d events lost: %v", len(evs), err)
			}
			evs = evs[:0]
		}
	}
	for {
		select {
		case a := <-s.in:
			if batch = append(batch, a); len(batch) >= 200 {
				write()
			}
		case e := <-s.evc:
			if evs = append(evs, e); len(evs) >= 200 {
				write()
			}
		case ack := <-s.flushc:
			for drained := false; !drained; {
				select {
				case a := <-s.in:
					batch = append(batch, a)
				case e := <-s.evc:
					evs = append(evs, e)
				default:
					drained = true
				}
			}
			write()
			close(ack)
		case <-tick.C:
			write()
		case <-pruneTick.C:
			s.prune(keepDays)
		case <-s.quit:
			write()
			return
		}
	}
}

// flush waits until everything queued so far is on disk. Tests and shutdown use it.
func (s *store) flush() {
	if s == nil {
		return
	}
	ack := make(chan struct{})
	s.flushc <- ack
	<-ack
}

func (s *store) close() {
	s.flush()
	close(s.quit)
	<-s.done
	s.db.Close()
}

func (s *store) insert(batch []attempt) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	stmt, err := tx.Prepare(`INSERT INTO attempts (ts, provider, model, status, class, latency_ms) VALUES (?, ?, ?, ?, ?, ?)`)
	if err != nil {
		tx.Rollback()
		return err
	}
	defer stmt.Close()
	for _, a := range batch {
		if _, err := stmt.Exec(a.ts.Unix(), a.provider, a.model, a.status, a.class, a.latencyMS); err != nil {
			tx.Rollback()
			return err
		}
	}
	return tx.Commit()
}

// prune drops attempts older than keepDays so the file cannot grow without bound.
func (s *store) prune(keepDays int) {
	cutoff := time.Now().AddDate(0, 0, -keepDays).Unix()
	for _, table := range []string{"attempts", "events"} {
		if _, err := s.db.Exec(`DELETE FROM `+table+` WHERE ts < ?`, cutoff); err != nil {
			log.Printf("stats: prune of %s failed: %v", table, err)
		}
	}
}

func (s *store) insertEvents(batch []event) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	stmt, err := tx.Prepare(`INSERT INTO events (ts, req_id, key_id, model, provider, status, result, flagged, filter, rule, score, ttfb_ms, total_ms, in_tok, out_tok, cost_u) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`)
	if err != nil {
		tx.Rollback()
		return err
	}
	defer stmt.Close()
	for _, e := range batch {
		fl := 0
		if e.Flagged {
			fl = 1
		}
		if _, err := stmt.Exec(e.TS.Unix(), e.ReqID, e.Key, e.Model, e.Provider, e.Status, e.Result, fl, e.Filter, e.Rule, e.Score, e.TTFBMS, e.TotalMS, e.InTok, e.OutTok, micro(e.CostUSD)); err != nil {
			tx.Rollback()
			return err
		}
	}
	return tx.Commit()
}

// eventFilter narrows listEvents. Zero values mean any.
type eventFilter struct {
	Since  time.Time
	Result string
	Key    string
	Q      string // matches a request id, a model, a key id or a key name
	Threat bool   // blocked, or flagged and let through
	// Attention is anything an operator should look at: a failure, a refusal, a block or a flag.
	// A bad request is the caller's mistake, so it is left out.
	Attention bool
	Limit     int
}

var likeEscaper = strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`)

// listEvents returns the newest events first.
func (s *store) listEvents(f eventFilter) ([]event, error) {
	q := `SELECT ts, req_id, key_id, model, provider, status, result, flagged, filter, rule, score, ttfb_ms, total_ms, in_tok, out_tok, cost_u FROM events WHERE ts >= ?`
	args := []any{f.Since.Unix()}
	if f.Result != "" {
		parts := strings.Split(f.Result, ",") // "rate_limited,model_denied" matches either
		if len(parts) > 8 {
			parts = parts[:8]
		}
		q += ` AND result IN (?` + strings.Repeat(`,?`, len(parts)-1) + `)`
		for _, p := range parts {
			args = append(args, strings.TrimSpace(p))
		}
	}
	if f.Attention {
		q += ` AND (result NOT IN ('ok', 'bad_request') OR flagged = 1)`
	}
	if f.Key != "" {
		q += ` AND key_id = ?`
		args = append(args, f.Key)
	}
	if f.Threat {
		q += ` AND (result = 'blocked' OR flagged = 1)`
	}
	if f.Q != "" {
		like := "%" + likeEscaper.Replace(f.Q) + "%"
		q += ` AND (req_id LIKE ? ESCAPE '\' OR model LIKE ? ESCAPE '\' OR key_id LIKE ? ESCAPE '\' OR key_id IN (SELECT id FROM keys WHERE name LIKE ? ESCAPE '\'))`
		args = append(args, like, like, like, like)
	}
	rows, err := s.db.Query(q+` ORDER BY ts DESC, rowid DESC LIMIT ?`, append(args, f.Limit)...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []event{}
	for rows.Next() {
		var e event
		var ts int64
		var fl int
		var cost int64
		if err := rows.Scan(&ts, &e.ReqID, &e.Key, &e.Model, &e.Provider, &e.Status, &e.Result, &fl, &e.Filter, &e.Rule, &e.Score, &e.TTFBMS, &e.TotalMS, &e.InTok, &e.OutTok, &cost); err != nil {
			return nil, err
		}
		e.TS, e.Flagged, e.CostUSD = time.Unix(ts, 0).UTC(), fl == 1, float64(cost)/1e6
		out = append(out, e)
	}
	return out, rows.Err()
}

type resultCount struct {
	Result  string
	Count   int
	Flagged int
	AvgMS   float64
}

type keyCount struct {
	Key             string
	Count, Blocked  int
	LimitedOrDenied int
	LastSeen        int64
	CostUSD         float64
}

// eventSummary counts events by result and by key since a time.
func (s *store) eventSummary(since time.Time) ([]resultCount, []keyCount, error) {
	rows, err := s.db.Query(`SELECT result, COUNT(*), COALESCE(SUM(flagged),0), COALESCE(AVG(total_ms),0) FROM events WHERE ts >= ? GROUP BY result ORDER BY result`, since.Unix())
	if err != nil {
		return nil, nil, err
	}
	var byResult []resultCount
	for rows.Next() {
		var r resultCount
		if err := rows.Scan(&r.Result, &r.Count, &r.Flagged, &r.AvgMS); err != nil {
			rows.Close()
			return nil, nil, err
		}
		byResult = append(byResult, r)
	}
	rows.Close()
	rows, err = s.db.Query(`SELECT key_id, COUNT(*), COALESCE(SUM(result='blocked'),0), COALESCE(SUM(result IN ('rate_limited','model_denied','over_budget')),0), MAX(ts), COALESCE(SUM(cost_u),0) FROM events WHERE ts >= ? GROUP BY key_id ORDER BY COUNT(*) DESC`, since.Unix())
	if err != nil {
		return nil, nil, err
	}
	defer rows.Close()
	var byKey []keyCount
	for rows.Next() {
		var k keyCount
		var c int64
		if err := rows.Scan(&k.Key, &k.Count, &k.Blocked, &k.LimitedOrDenied, &k.LastSeen, &c); err != nil {
			return nil, nil, err
		}
		k.CostUSD = float64(c) / 1e6
		byKey = append(byKey, k)
	}
	return byResult, byKey, rows.Err()
}

type totals struct {
	Requests, Blocked, Flagged, ProviderErrors, Refused int
	InTok, OutTok                                       int64
	CostUSD                                             float64
}

// totals counts events in [from, to). The admin page compares this period with the one before it.
func (s *store) totals(from, to time.Time) (t totals, err error) {
	var cost int64
	err = s.db.QueryRow(`SELECT COUNT(*), COALESCE(SUM(result='blocked'),0), COALESCE(SUM(flagged),0), COALESCE(SUM(result='provider_error'),0),
		COALESCE(SUM(result IN ('rate_limited','model_denied','too_large','over_budget')),0),
		COALESCE(SUM(in_tok),0), COALESCE(SUM(out_tok),0), COALESCE(SUM(cost_u),0) FROM events WHERE ts >= ? AND ts < ?`, from.Unix(), to.Unix()).
		Scan(&t.Requests, &t.Blocked, &t.Flagged, &t.ProviderErrors, &t.Refused, &t.InTok, &t.OutTok, &cost)
	t.CostUSD = float64(cost) / 1e6
	return
}

// firstByte returns the median and 95th percentile time to the provider's first byte for answered
// requests, and how many there were. First byte is used because a streamed answer's total time
// depends on how long the answer is.
func (s *store) firstByte(since time.Time) (p50, p95 int64, n int, err error) {
	if err = s.db.QueryRow(`SELECT COUNT(*) FROM events WHERE ts >= ? AND result = 'ok'`, since.Unix()).Scan(&n); err != nil || n == 0 {
		return
	}
	at := func(p float64) (v int64, err error) {
		err = s.db.QueryRow(`SELECT ttfb_ms FROM events WHERE ts >= ? AND result = 'ok' ORDER BY ttfb_ms LIMIT 1 OFFSET ?`, since.Unix(), int(float64(n-1)*p)).Scan(&v)
		return
	}
	if p50, err = at(0.5); err != nil {
		return
	}
	p95, err = at(0.95)
	return
}

type ruleCount struct {
	Filter, Rule   string
	Count, Blocked int
}

// rules counts what the filter caught, by the rule that matched, most common first.
func (s *store) rules(since time.Time) ([]ruleCount, error) {
	rows, err := s.db.Query(`SELECT filter, rule, COUNT(*), COALESCE(SUM(result='blocked'),0) FROM events
		WHERE ts >= ? AND filter != '' AND (result = 'blocked' OR flagged = 1) GROUP BY filter, rule ORDER BY COUNT(*) DESC, rule LIMIT 20`, since.Unix())
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []ruleCount{}
	for rows.Next() {
		var r ruleCount
		if err := rows.Scan(&r.Filter, &r.Rule, &r.Count, &r.Blocked); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

type seriesBucket struct {
	T                                    int64 `json:"t"`
	Total, OK, Blocked, Flagged, Limited int
	Errors                               int
}

// series counts events in n equal buckets of step seconds starting at from, for the traffic chart.
func (s *store) series(from time.Time, step int64, n int) ([]seriesBucket, error) {
	out := make([]seriesBucket, n)
	for i := range out {
		out[i].T = from.Unix() + int64(i)*step
	}
	rows, err := s.db.Query(`SELECT (ts - ?) / ?, COUNT(*), COALESCE(SUM(result='ok'),0), COALESCE(SUM(result='blocked'),0), COALESCE(SUM(flagged),0),
		COALESCE(SUM(result IN ('rate_limited','model_denied','too_large','over_budget')),0), COALESCE(SUM(result='provider_error'),0) FROM events WHERE ts >= ? GROUP BY 1`,
		from.Unix(), step, from.Unix())
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var i int64
		var b seriesBucket
		if err := rows.Scan(&i, &b.Total, &b.OK, &b.Blocked, &b.Flagged, &b.Limited, &b.Errors); err != nil {
			return nil, err
		}
		if i >= int64(n) {
			i = int64(n) - 1 // a request that arrived while this was running
		}
		if i >= 0 {
			b.T = out[i].T
			out[i].Total += b.Total
			out[i].OK += b.OK
			out[i].Blocked += b.Blocked
			out[i].Flagged += b.Flagged
			out[i].Limited += b.Limited
			out[i].Errors += b.Errors
		}
	}
	return out, rows.Err()
}

func (s *store) insertKey(r keyRec, hash string) error {
	_, err := s.db.Exec(`INSERT INTO keys (id, name, hash, prefix, created, revoked, models, rpm, budget) VALUES (?,?,?,?,?,0,?,?,?)`,
		r.ID, r.Name, hash, r.Prefix, r.Created.Unix(), strings.Join(r.Models, ","), r.RPM, r.BudgetUSD)
	return err
}

func (s *store) setRevoked(id string) (bool, error) {
	res, err := s.db.Exec(`UPDATE keys SET revoked = 1 WHERE id = ? AND revoked = 0`, id)
	if err != nil {
		return false, err
	}
	n, _ := res.RowsAffected()
	return n == 1, nil
}

// allKeys returns every key, revoked ones included, with the hash for the cache.
func (s *store) allKeys() (recs []keyRec, hashes []string, err error) {
	rows, err := s.db.Query(`SELECT id, name, hash, prefix, created, revoked, models, rpm, budget FROM keys ORDER BY created, id`)
	if err != nil {
		return nil, nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var r keyRec
		var h, models string
		var created int64
		var revoked int
		if err := rows.Scan(&r.ID, &r.Name, &h, &r.Prefix, &created, &revoked, &models, &r.RPM, &r.BudgetUSD); err != nil {
			return nil, nil, err
		}
		r.Created, r.Revoked = time.Unix(created, 0).UTC(), revoked == 1
		if models != "" {
			r.Models = strings.Split(models, ",")
		}
		recs, hashes = append(recs, r), append(hashes, h)
	}
	return recs, hashes, rows.Err()
}

// statRow is the outcome count for one provider, or one provider and model.
type statRow struct {
	Provider, Model string
	Requests        int
	ByClass         map[string]int
	AvgTTFB         *float64 // over successful tries only
}

func (r statRow) errors() int { return r.Requests - r.ByClass["ok"] }

const statsSQL = `SELECT provider, %s, COUNT(*),
	COALESCE(SUM(class='ok'),0), COALESCE(SUM(class='rate_limited'),0), COALESCE(SUM(class='server_error'),0),
	COALESCE(SUM(class='unreachable'),0), COALESCE(SUM(class='client_error'),0), COALESCE(SUM(class='stream_error'),0),
	AVG(CASE WHEN class='ok' THEN latency_ms END)
	FROM attempts WHERE ts >= ? GROUP BY %s ORDER BY %s`

// stats counts attempts since a time. byModel false groups by provider only.
func (s *store) stats(since time.Time, byModel bool) ([]statRow, error) {
	modelCol, group := "''", "provider"
	if byModel {
		modelCol, group = "model", "provider, model"
	}
	rows, err := s.db.Query(fmt.Sprintf(statsSQL, modelCol, group, group), since.Unix())
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []statRow
	for rows.Next() {
		var r statRow
		var ok, rl, se, un, ce, st int
		var avg sql.NullFloat64
		if err := rows.Scan(&r.Provider, &r.Model, &r.Requests, &ok, &rl, &se, &un, &ce, &st, &avg); err != nil {
			return nil, err
		}
		r.ByClass = map[string]int{"ok": ok, "rate_limited": rl, "server_error": se, "unreachable": un, "client_error": ce, "stream_error": st}
		if avg.Valid {
			r.AvgTTFB = &avg.Float64
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

type refreshInfo struct {
	TS    int64
	OK    bool
	Error string
	Count int
}

// saveModels replaces a provider's model list and notes the refresh as successful.
func (s *store) saveModels(provider string, ids []string, now time.Time) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.Exec(`DELETE FROM models WHERE provider = ?`, provider); err != nil {
		return err
	}
	seen := map[string]bool{}
	for _, id := range ids {
		if id == "" || seen[id] {
			continue
		}
		seen[id] = true
		if _, err := tx.Exec(`INSERT INTO models (provider, id) VALUES (?, ?)`, provider, id); err != nil {
			return err
		}
	}
	if _, err := tx.Exec(`INSERT INTO refreshes (provider, ts, ok, error, count) VALUES (?, ?, 1, '', ?)
		ON CONFLICT(provider) DO UPDATE SET ts = excluded.ts, ok = 1, error = '', count = excluded.count`, provider, now.Unix(), len(seen)); err != nil {
		return err
	}
	return tx.Commit()
}

// saveRefreshError notes a failed refresh and keeps the old model list.
func (s *store) saveRefreshError(provider, msg string, now time.Time) error {
	_, err := s.db.Exec(`INSERT INTO refreshes (provider, ts, ok, error, count) VALUES (?, ?, 0, ?, 0)
		ON CONFLICT(provider) DO UPDATE SET ts = excluded.ts, ok = 0, error = excluded.error`, provider, now.Unix(), msg)
	return err
}

// models returns each provider's listed model ids, sorted.
func (s *store) models() (map[string][]string, error) {
	rows, err := s.db.Query(`SELECT provider, id FROM models ORDER BY provider, id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string][]string{}
	for rows.Next() {
		var p, id string
		if err := rows.Scan(&p, &id); err != nil {
			return nil, err
		}
		out[p] = append(out[p], id)
	}
	for _, ids := range out {
		sort.Strings(ids)
	}
	return out, rows.Err()
}

func (s *store) refreshes() (map[string]refreshInfo, error) {
	rows, err := s.db.Query(`SELECT provider, ts, ok, error, count FROM refreshes`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]refreshInfo{}
	for rows.Next() {
		var p string
		var r refreshInfo
		var ok int
		if err := rows.Scan(&p, &r.TS, &ok, &r.Error, &r.Count); err != nil {
			return nil, err
		}
		r.OK = ok == 1
		out[p] = r
	}
	return out, rows.Err()
}

// spentSince sums what each key has cost since a time, in dollars.
func (s *store) spentSince(from time.Time) (map[string]float64, error) {
	rows, err := s.db.Query(`SELECT key_id, SUM(cost_u) FROM events WHERE ts >= ? AND cost_u > 0 GROUP BY key_id`, from.Unix())
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]float64{}
	for rows.Next() {
		var id string
		var c int64
		if err := rows.Scan(&id, &c); err != nil {
			return nil, err
		}
		out[id] = float64(c) / 1e6
	}
	return out, rows.Err()
}
