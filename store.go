// Stats and model lists, kept in SQLite. This is the only file that touches the database.
// What is stored: provider, model, HTTP status, outcome class, time to first byte and a timestamp.
// Never prompts, answers, keys or client details.
package main

import (
	"database/sql"
	"fmt"
	"log"
	"sort"
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
	error TEXT NOT NULL DEFAULT '', count INTEGER NOT NULL DEFAULT 0);`

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
	s := &store{db: db, in: make(chan attempt, 1024), flushc: make(chan chan struct{}), quit: make(chan struct{}), done: make(chan struct{})}
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

// run writes queued attempts in batches, once a second or every 200 rows.
func (s *store) run(keepDays int) {
	defer close(s.done)
	tick, pruneTick := time.NewTicker(time.Second), time.NewTicker(time.Hour)
	defer tick.Stop()
	defer pruneTick.Stop()
	var batch []attempt
	write := func() {
		if len(batch) == 0 {
			return
		}
		if err := s.insert(batch); err != nil {
			log.Printf("stats: %d attempts lost: %v", len(batch), err)
		}
		batch = batch[:0]
	}
	for {
		select {
		case a := <-s.in:
			if batch = append(batch, a); len(batch) >= 200 {
				write()
			}
		case ack := <-s.flushc:
			for drained := false; !drained; {
				select {
				case a := <-s.in:
					batch = append(batch, a)
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
	if _, err := s.db.Exec(`DELETE FROM attempts WHERE ts < ?`, cutoff); err != nil {
		log.Printf("stats: prune failed: %v", err)
	}
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
