// One event per chat request: who asked, what came back, what the filter said, how long it took.
// Events go to SQLite (for the admin page) and, if ACCESS_LOG is set, out as one JSON line each.
// Never prompts, answers or keys.
package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"math"
	"net/http"
	"os"
	"sync/atomic"
	"time"
)

// result says how the request ended. Everything but "ok" is worth a look.
//
//	ok              answered by a provider
//	provider_error  the provider failed or was unreachable
//	blocked         the filter refused it
//	rate_limited    the key went over its requests per minute
//	model_denied    the key may not use that model
//	over_budget     the key has used its monthly budget
//	too_large       body or new text over the limit
//	bad_request     not valid JSON, or an unknown provider
type event struct {
	TS       time.Time `json:"ts"`
	ReqID    string    `json:"request_id"`
	Key      string    `json:"key"` // key id, or "master"
	Model    string    `json:"model"`
	Provider string    `json:"provider,omitempty"`
	Status   int       `json:"status"`
	Result   string    `json:"result"`
	Flagged  bool      `json:"flagged,omitempty"`
	Filter   string    `json:"filter,omitempty"`
	Rule     string    `json:"rule,omitempty"` // for the operator; clients never see it
	Score    float64   `json:"score,omitempty"`
	TTFBMS   int64     `json:"ttfb_ms"`
	TotalMS  int64     `json:"total_ms"`
	InTok    int       `json:"in_tokens,omitempty"`
	OutTok   int       `json:"out_tokens,omitempty"`
	CostUSD  float64   `json:"cost_usd,omitempty"` // 0 when the model has no price
}

// reqInfo is filled in by the chat handler while it works and read once by logged() at the end.
type reqInfo struct {
	id, model, provider, result string
	used                        string // provider/model that answered
	inTok, outTok               int
	estIn                       int // request bytes over four: charged when the provider reports no tokens
	v                           verdict
	flagged                     bool
	ttfb                        int64
}

type ctxKey int

const (
	infoKey ctxKey = iota
	identKey
)

func infoOf(r *http.Request) *reqInfo {
	if ri, ok := r.Context().Value(infoKey).(*reqInfo); ok {
		return ri
	}
	return &reqInfo{} // handler used without logged(): writes go nowhere
}

type statusWriter struct {
	http.ResponseWriter
	status int
}

func (w *statusWriter) WriteHeader(code int) {
	w.status = code
	w.ResponseWriter.WriteHeader(code)
}

// Unwrap lets http.NewResponseController reach the real writer to flush streamed answers.
func (w *statusWriter) Unwrap() http.ResponseWriter { return w.ResponseWriter }

func newReqID() string {
	b := make([]byte, 6)
	rand.Read(b)
	return hex.EncodeToString(b)
}

// logged wraps the chat handler: it gives the request an id, then records one event when it ends.
func logged(st *store, next http.HandlerFunc) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ri := &reqInfo{id: newReqID()}
		w.Header().Set("X-Gateway-Request-Id", ri.id)
		sw := &statusWriter{ResponseWriter: w, status: http.StatusOK}
		start := time.Now()
		next(sw, r.WithContext(context.WithValue(r.Context(), infoKey, ri)))
		result := ri.result
		if result == "" {
			result = "ok"
			if sw.status >= 400 {
				result = "provider_error"
			}
		}
		e := event{TS: start, ReqID: ri.id, Key: identOf(r).ID, Model: ri.model, Provider: ri.provider, Status: sw.status,
			Result: result, Flagged: ri.flagged, Filter: ri.v.filter, Rule: ri.v.rule, Score: ri.v.score,
			TTFBMS: ri.ttfb, TotalMS: time.Since(start).Milliseconds(), InTok: ri.inTok, OutTok: ri.outTok}
		if ri.used != "" && ri.inTok+ri.outTok == 0 {
			// The provider reported nothing: a client hung up before the final usage chunk, or the provider sends none.
			// Charge the input at four bytes a token rather than book the request as free.
			e.InTok, ri.inTok = ri.estIn, ri.estIn
		}
		if p, ok := priceOf(ri.used); ok && ri.used != "" {
			e.CostUSD = math.Round(p.cost(ri.inTok, ri.outTok)*1e6) / 1e6
			if k := identOf(r).Key; k != nil && e.CostUSD > 0 {
				st.keys.addSpend(k.ID, e.CostUSD)
			}
		}
		if e.Score < 0.01 { // nothing matched: keep the line short
			e.Filter, e.Rule, e.Score = "", "", 0
		}
		access.write(e)
		st.recordEvent(e)
	})
}

// sink writes events as JSON lines from its own goroutine, so a slow or full pipe never slows a request.
// If the queue fills, lines are dropped and counted rather than waiting.
type sink struct {
	ch      chan []byte
	done    chan struct{}
	w       io.Writer
	closer  io.Closer
	dropped atomic.Int64
}

var access *sink // nil means the access log is off

// openAccessLog reads ACCESS_LOG: empty (off), stdout, stderr or a file path (appended to).
func openAccessLog(spec string) (*sink, error) {
	s := &sink{ch: make(chan []byte, 4096), done: make(chan struct{})}
	switch spec {
	case "":
		return nil, nil
	case "stdout":
		s.w = os.Stdout
	case "stderr":
		s.w = os.Stderr
	default:
		f, err := os.OpenFile(spec, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
		if err != nil {
			return nil, fmt.Errorf("ACCESS_LOG: %w", err)
		}
		s.w, s.closer = f, f
	}
	go func() {
		defer close(s.done)
		for line := range s.ch {
			if _, err := s.w.Write(line); err != nil {
				log.Printf("access log write failed: %v", err)
			}
		}
	}()
	return s, nil
}

func (s *sink) write(e event) {
	if s == nil {
		return
	}
	b, err := json.Marshal(e)
	if err != nil {
		return
	}
	select {
	case s.ch <- append(b, '\n'):
	default:
		s.dropped.Add(1)
	}
}

// close drains the queue and waits for it to be written. Tests and shutdown use it.
func (s *sink) close() {
	if s == nil {
		return
	}
	close(s.ch)
	<-s.done
	if s.closer != nil {
		s.closer.Close()
	}
}
