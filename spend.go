// Spend: what each request cost, and a monthly budget per virtual key.
// The gateway ships no prices, because they change and a stale one is worse than none. You supply them in a
// JSON file (PRICES_FILE) of US dollars per million tokens, keyed by provider/model. A trailing * matches a prefix.
//
//	{"openai/gpt-4o-mini": {"in": 0.15, "out": 0.60}, "ollama/*": {"in": 0, "out": 0}}
//
// A key with a budget may only use priced models, so a model with no price can never be a way around the budget.
package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"math"
	"os"
	"strings"
	"time"
)

type price struct {
	In  float64 `json:"in"`
	Out float64 `json:"out"`
}

var prices map[string]price // set once at start; tests replace it

func loadPrices(path string) (map[string]price, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("PRICES_FILE: %w", err)
	}
	m := map[string]price{}
	if err := json.Unmarshal(b, &m); err != nil {
		return nil, fmt.Errorf("PRICES_FILE: %w", err)
	}
	for k, p := range m {
		if p.In < 0 || p.Out < 0 || !modelPattern.MatchString(k) {
			return nil, fmt.Errorf("PRICES_FILE: bad entry %q", k)
		}
	}
	return m, nil
}

// priceOf finds the price for "provider/model": an exact entry first, then the longest prefix pattern.
func priceOf(full string) (price, bool) {
	if p, ok := prices[full]; ok {
		return p, true
	}
	best, found := "", price{}
	for k, p := range prices {
		if pre, ok := strings.CutSuffix(k, "*"); ok && strings.HasPrefix(full, pre) && len(k) > len(best) {
			best, found = k, p
		}
	}
	return found, best != ""
}

func (p price) cost(in, out int) float64 {
	return (float64(in)*p.In + float64(out)*p.Out) / 1e6
}

func monthStart(t time.Time) time.Time {
	t = t.UTC()
	return time.Date(t.Year(), t.Month(), 1, 0, 0, 0, 0, time.UTC)
}

// usageTail keeps the last bytes of a provider answer. The token counts sit at the end of both a plain
// answer and a stream (the usage chunk is the last one), so this is all that needs to be read.
// ponytail: a provider that puts usage first in a long plain answer is missed, and its tokens count as zero.
type usageTail struct{ b []byte }

const tailSize = 32 << 10

func (u *usageTail) write(p []byte) {
	u.b = append(u.b, p...)
	if len(u.b) > tailSize {
		u.b = append(u.b[:0], u.b[len(u.b)-tailSize:]...)
	}
}

func (u *usageTail) tokens() (in, out int) {
	i := bytes.LastIndex(u.b, []byte(`"usage"`))
	if i < 0 {
		return 0, 0
	}
	rest := u.b[i+len(`"usage"`):]
	j := bytes.IndexByte(rest, '{')
	if j < 0 {
		return 0, 0
	}
	var c chatUsage
	json.NewDecoder(bytes.NewReader(rest[j:])).Decode(&c)
	return c.PromptTokens, c.CompletionTokens
}

// chatUsage is the part of a chat answer or chunk that carries token counts.
type chatUsage struct {
	PromptTokens     int `json:"prompt_tokens"`
	CompletionTokens int `json:"completion_tokens"`
}

// micro turns dollars into whole millionths, so sums in the database do not drift.
func micro(usd float64) int64 { return int64(math.Round(usd * 1e6)) }
