// Stage 3: filters score text, the policy decides block, log or pass.
package main

import (
	"encoding/base64"
	"encoding/json"
	"log"
	"math"
	"os"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"
)

type filter struct {
	name  string
	score func(text string) (score float64, rule string)
}

var builtin = map[string]filter{"tokens": {"tokens", tokensScore}}

type policy struct {
	filters    []filter
	block, log float64
	maxScan    int  // bytes of new text one request may carry; 0 means no limit
	scanSystem bool // also scan system and developer messages
}

type verdict struct {
	filter, rule string
	score        float64
}

func defaultPolicy() policy {
	return policy{filters: []filter{builtin["tokens"]}, block: 0.8, log: 0.4, maxScan: 2 << 20}
}

// FILTERS=tokens,other runs in that order; FILTERS=none turns filtering off.
func loadPolicy() policy {
	p := defaultPolicy()
	if names := os.Getenv("FILTERS"); names != "" {
		p.filters = nil
		for _, n := range strings.Split(names, ",") {
			n = strings.TrimSpace(n)
			if n == "none" {
				continue
			}
			f, ok := builtin[n]
			if !ok {
				log.Fatalf("unknown filter %q in FILTERS, available: tokens, none", n)
			}
			p.filters = append(p.filters, f)
		}
	}
	p.block, p.log = envFloat("FILTER_BLOCK", p.block), envFloat("FILTER_LOG", p.log)
	if mb := envFloat("SCAN_LIMIT_MB", float64(p.maxScan)/(1<<20)); mb < 0 {
		log.Fatal("SCAN_LIMIT_MB must be 0 or more (0 means no limit)")
	} else {
		p.maxScan = int(mb * (1 << 20))
	}
	p.scanSystem = os.Getenv("SCAN_SYSTEM") == "1"
	if !(0 < p.log && p.log <= p.block && p.block <= 1) {
		log.Fatal("need 0 < FILTER_LOG <= FILTER_BLOCK <= 1")
	}
	return p
}

func envFloat(name string, def float64) float64 {
	s := os.Getenv(name)
	if s == "" {
		return def
	}
	f, err := strconv.ParseFloat(s, 64)
	if err != nil {
		log.Fatalf("%s is not a number: %q", name, s)
	}
	return f
}

// check returns the worst verdict and stops early once something is blocked.
// The zero verdict (empty filter) means nothing scored above zero.
func (p policy) check(texts []string) verdict {
	var worst verdict
	for _, f := range p.filters {
		for _, t := range texts {
			s, rule := f.score(t)
			if s = math.Round(s*100) / 100; s > worst.score {
				worst = verdict{f.name, rule, s}
			}
			if worst.score >= p.block {
				return worst
			}
		}
	}
	return worst
}

// newTexts returns the text that arrived since the last assistant message:
// the user turn and tool results. Older history already passed through earlier,
// and rescanning it would block every later request after one bad tool result.
// System and developer messages are the app's own words, so they are skipped
// unless scanSystem is set (SCAN_SYSTEM=1, for apps that build them from user data).
// The pieces come back joined into one text, so a phrase split across messages still matches.
func newTexts(messages json.RawMessage, scanSystem bool) []string {
	var msgs []struct {
		Role    string          `json:"role"`
		Name    string          `json:"name"`
		Content json.RawMessage `json:"content"`
	}
	if json.Unmarshal(messages, &msgs) != nil {
		return nil // not our job to reject malformed input, the provider will
	}
	start := 0
	for i := len(msgs) - 1; i >= 0; i-- {
		if msgs[i].Role == "assistant" {
			start = i + 1
			break
		}
	}
	var out []string
	for _, m := range msgs[start:] {
		if !scanSystem && (m.Role == "system" || m.Role == "developer") {
			continue
		}
		if m.Name != "" {
			out = append(out, m.Name)
		}
		var v any
		if json.Unmarshal(m.Content, &v) == nil {
			collect(v, &out)
		}
	}
	if len(out) == 0 {
		return nil
	}
	return []string{strings.Join(out, "\n")}
}

// collect gathers every string inside content, whatever the key is called: tool results come
// in shapes nobody can list in advance, so a fixed list of keys would be a way past the filter.
// ponytail: tool-call arguments in assistant messages and image data are not scanned.
func collect(v any, out *[]string) {
	switch x := v.(type) {
	case string:
		*out = append(*out, x)
	case []any:
		for _, e := range x {
			collect(e, out)
		}
	case map[string]any:
		keys := make([]string, 0, len(x))
		for k := range x {
			keys = append(keys, k)
		}
		sort.Strings(keys) // same request, same scan order
		for _, k := range keys {
			collect(x[k], out)
		}
	}
}

type rule struct {
	name string
	re   *regexp.Regexp
	w    float64
	raw  bool // match lowercased raw text instead of the word-normalised text
}

func r(name, pattern string, w float64, raw bool) rule {
	return rule{name, regexp.MustCompile(pattern), w, raw}
}

// ponytail: static phrases only. Paraphrase, other languages, encodings and look-alike
// letters get past it; that is what the AI filter is for. Linear-time regexp, so no ReDoS.
var tokenRules = []rule{
	r("ignore-instructions", `\b(?:ignore|disregard|forget|override|bypass) (?:\w+ ){0,3}?(?:previous|prior|above|earlier|your|all|system|these|any) (?:\w+ ){0,2}?(?:instructions|rules|guidelines|prompts?|directions)\b`, 0.9, false),
	r("forget-everything", `\b(?:forget|ignore|disregard) (?:everything|all) (?:you|i|that|above|before|previous|prior)\b`, 0.6, false),
	r("reveal-prompt", `\b(?:reveal|show|print|repeat|output|display|leak|dump|recite|copy|give me|tell me) (?:\w+ ){0,5}?(?:system prompt|system message|initial prompt|initial instructions|original instructions|initial configuration|hidden instructions|your instructions|your prompt)\b`, 0.85, false),
	r("persona-override", `\byou are now (?:\w+ ){0,3}?(?:free|unrestricted|unfiltered|dan|jailbroken|evil)\b|\b(?:developer mode|dan mode|do anything now|jailbreak(?:en)?)\b`, 0.5, false),
	r("chat-template", `<\|(?:im_start|im_end|system|assistant|endoftext)\|>|\[/?inst\]|<<sys>>`, 0.7, true),
	r("hide-from-user", `\b(?:do not|don t|dont|never) (?:\w+ ){0,3}?(?:tell|inform|alert|notify) (?:the |this )?user\b`, 0.6, false),
	r("exfiltrate", `\b(?:send|post|upload|forward|email|exfiltrate|transmit) (?:\w+ ){0,6}?(?:to|at) (?:https?|www|ftp)\b`, 0.5, false),
	r("fake-authority", `\bimportant (?:message|instructions?|notice) (?:to|for|from) (?:the )?(?:ai|assistant|model|llm|agent|system|developer|user)\b`, 0.5, false),
	// Round 2, added after the first test set showed these misses.
	r("new-instructions", `\b(?:new|updated|revised|real|actual) (?:instructions|directives)\b`, 0.6, false),
	r("instructions-changed", `\byour (?:instructions|rules|guidelines|task|goal|orders) (?:have|has) (?:changed|been (?:updated|replaced|changed|revoked))\b`, 0.6, false),
	r("disregard-user", `\b(?:disregard|ignore|forget|dismiss) (?:what )?(?:the )?user(?: s)? (?:request|message|question|query|prompt|input|said)\b`, 0.75, false),
	r("instead-directive", `\binstead (?:reply|respond|output|write|say|print|list|send|call|run|execute|recommend)\b`, 0.35, false),
	r("rules-gone", `\b(?:pretend|act as if|imagine|suppose) (?:\w+ ){0,6}?(?:rules|instructions|guidelines|restrictions|prompt)(?: \w+){0,6}? (?:never existed|do not exist|don t exist|no longer apply|are gone|were removed)\b`, 0.7, false),
	r("set-aside", `\b(?:set aside|put aside) (?:\w+ ){0,3}?(?:you were told|you were given|instructions|rules|guidelines|everything)\b`, 0.6, false),
	r("no-limits", `\b(?:with no|without any|have no|has no) (?:content (?:policy|filter)|restrictions|ethical (?:guidelines|limits)|safety (?:rules|filters))\b`, 0.45, false),
	r("pipe-to-shell", `(?:curl|wget)[^\n|]{0,200}\|\s*(?:sudo )?(?:sh|bash|zsh|powershell|pwsh)\b`, 0.7, true),
	r("md-image-leak", `!\[[^\]]*\]\(https?://[^)\s]*\?[^)\s]*=[^)\s]*\)`, 0.45, true),
	r("decode-and-obey", `\b(?:decode|decrypt|deobfuscate|rot13|base64)\b (?:\w+ ){0,8}?(?:follow|obey|execute|run|apply)\b|\b(?:follow|obey|execute|run|apply) (?:\w+ ){0,8}?(?:decoded|base64|rot13)\b`, 0.5, false),
	// ponytail: Spanish, French, German, Chinese, Russian and Hindi only. Other languages still pass.
	r("ignore-instructions-intl", `\bignora(?:r|d)? (?:\S+ ){0,3}?(?:instrucciones|indicaciones)\b|\bignore[rz] (?:\S+ ){0,3}?(?:instructions|consignes)\b|\boubliez (?:\S+ ){0,3}?(?:instructions|consignes)\b|\bignorier(?:e|en) (?:\S+ ){0,3}?(?:anweisungen|instruktionen|regeln|befehle)\b|\bvergiss (?:\S+ ){0,3}?(?:anweisungen|instruktionen)\b|忽略.{0,10}?(?:指令|指示|规则|規則)|(?:игнорир|забудь)\S* (?:\S+ ){0,3}?(?:инструкци|правил)|निर्देश\S* (?:\S+ ){0,2}?(?:अनदेखा|नजरअंदाज)`, 0.85, false),
}

// needs gives, per rule, anchor literals: the regexp cannot match without one of them, so every
// match lies next to one. For the rules over words, the regexp only runs on a window around each
// anchor (matchNear), not on the whole text. This is what makes real documents and code fast: the
// regexp engine was about 87% of the time when it scanned everything. Anchors are the rarest
// required group of each rule. TestPrefilterChangesNothing proves the result never changes.
var needs = map[string][]string{
	"ignore-instructions":      {"ignore", "disregard", "forget", "override", "bypass"},
	"forget-everything":        {"forget everything", "forget all", "ignore everything", "ignore all", "disregard everything", "disregard all"},
	"reveal-prompt":            {"system prompt", "system message", "initial prompt", "initial instructions", "original instructions", "initial configuration", "hidden instructions", "your instructions", "your prompt"},
	"persona-override":         {"you are now", "developer mode", "dan mode", "do anything now", "jailbreak", "jailbreaken"},
	"chat-template":            {"<|", "[inst]", "[/inst]", "<<sys>>"},
	"hide-from-user":           {"tell the user", "tell this user", "tell user", "inform the user", "inform this user", "inform user", "alert the user", "alert this user", "alert user", "notify the user", "notify this user", "notify user"},
	"exfiltrate":               {"to http", "to https", "at http", "at https", "to www", "at www", "to ftp", "at ftp"},
	"fake-authority":           {"important message", "important instruction", "important instructions", "important notice"},
	"new-instructions":         {"new instructions", "updated instructions", "revised instructions", "real instructions", "actual instructions", "new directives", "updated directives", "revised directives", "real directives", "actual directives"},
	"instructions-changed":     {"your instructions", "your rules", "your guidelines", "your task", "your goal", "your orders"},
	"disregard-user":           {"disregard user", "disregard the user", "disregard what", "ignore user", "ignore the user", "ignore what", "forget user", "forget the user", "forget what", "dismiss user", "dismiss the user", "dismiss what"},
	"instead-directive":        {"instead reply", "instead respond", "instead output", "instead write", "instead say", "instead print", "instead list", "instead send", "instead call", "instead run", "instead execute", "instead recommend"},
	"rules-gone":               {"never existed", "do not exist", "don t exist", "no longer apply", "are gone", "were removed"},
	"set-aside":                {"set aside", "put aside"},
	"no-limits":                {"content policy", "content filter", "restrictions", "ethical guidelines", "ethical limits", "safety rules", "safety filters"},
	"pipe-to-shell":            {"curl", "wget"},
	"md-image-leak":            {"!["},
	"decode-and-obey":          {"decode", "decoded", "decrypt", "deobfuscate", "rot13", "base64"},
	"ignore-instructions-intl": {"ignora", "ignorar", "ignorad", "ignorer", "ignorez", "oubliez", "ignoriere", "ignorieren", "vergiss", "忽略", "игнорир", "забудь", "निर्देश"},
}
var prefilter = true // tests switch it off to prove it never changes a result

// windowWords is how many words each side of an anchor a window covers. The longest match any
// rule can make is under 17 words, so a window always holds the whole match.
const windowWords = 24

type anchorRef struct {
	rule   int
	phrase string
}

var (
	wordAnchors = map[string][]anchorRef{} // first word of an ASCII anchor -> the anchors that start with it
	subAnchors  = map[int][]string{}       // non-ASCII anchors per rule, found by substring search
)

func init() {
	for ri, ru := range tokenRules {
		if ru.raw {
			continue
		}
		for _, a := range needs[ru.name] {
			if isWordAnchor(a) {
				first, _, _ := strings.Cut(a, " ")
				wordAnchors[first] = append(wordAnchors[first], anchorRef{ri, a})
			} else {
				subAnchors[ri] = append(subAnchors[ri], a)
			}
		}
	}
}

// isWordAnchor reports whether a is plain lowercase ASCII words, which must appear as whole words.
func isWordAnchor(a string) bool {
	for i := 0; i < len(a); i++ {
		if c := a[i]; !(c >= 'a' && c <= 'z' || c >= '0' && c <= '9' || c == ' ') {
			return false
		}
	}
	return true
}

// anchorHits scans the word text once and returns, per rule, where its anchor phrases sit
// (sorted by position). Looking up each word in a map is far cheaper than searching the whole text
// for every anchor phrase one after another, which was most of the time on real documents.
func anchorHits(src string) [][][2]int {
	hits := make([][][2]int, len(tokenRules))
	for i := 0; i < len(src); {
		j := strings.IndexByte(src[i:], ' ')
		if j < 0 {
			j = len(src) - i
		}
		for _, ref := range wordAnchors[src[i:i+j]] {
			end := i + len(ref.phrase)
			if strings.HasPrefix(src[i:], ref.phrase) && (end == len(src) || src[end] == ' ') {
				hits[ref.rule] = append(hits[ref.rule], [2]int{i, end})
			}
		}
		i += j + 1
	}
	for ri, subs := range subAnchors {
		for _, a := range subs {
			for off := 0; off < len(src); {
				i := strings.Index(src[off:], a)
				if i < 0 {
					break
				}
				hits[ri] = append(hits[ri], [2]int{off + i, off + i + len(a)})
				off += i + 1
			}
		}
		sort.Slice(hits[ri], func(a, b int) bool { return hits[ri][a][0] < hits[ri][b][0] })
	}
	return hits
}

// matchWindows reports whether re matches src near any of hits. Windows start and end at spaces,
// so \b behaves exactly as it would on the full text. Overlapping windows are merged, so the regexp
// never reads more than the whole text once.
func matchWindows(re *regexp.Regexp, src string, hits [][2]int) bool {
	lo, hi := -1, -1
	for _, h := range hits {
		s, e := wordsBack(src, h[0]), wordsForward(src, h[1])
		if lo < 0 {
			lo, hi = s, e
			continue
		}
		if s <= hi {
			if e > hi {
				hi = e
			}
			continue
		}
		if re.MatchString(src[lo:hi]) {
			return true
		}
		lo, hi = s, e
	}
	return lo >= 0 && re.MatchString(src[lo:hi])
}

// wordsBack returns the start of the window: windowWords words before pos, right after a space.
func wordsBack(s string, pos int) int {
	for n := windowWords; n > 0; n-- {
		sp := strings.LastIndexByte(s[:pos], ' ')
		if sp < 0 {
			return 0
		}
		pos = sp
	}
	return pos + 1
}

// wordsForward returns the end of the window: windowWords words after pos, at a space.
func wordsForward(s string, pos int) int {
	for n := windowWords; n > 0; n-- {
		sp := strings.IndexByte(s[pos:], ' ')
		if sp < 0 {
			return len(s)
		}
		pos += sp + 1
	}
	return pos - 1
}
func containsAny(s string, subs []string) bool {
	for _, sub := range subs {
		if strings.Contains(s, sub) {
			return true
		}
	}
	return false
}

// normWords keeps letters, digits and combining marks, and joins the runs with single spaces.
// One pass into one buffer; the FieldsFunc and Join version built a list of every word first.
func normWords(s string) string {
	var b strings.Builder
	b.Grow(len(s))
	gap := false
	for _, c := range s {
		var word bool
		if c < 0x80 { // ASCII fast path
			word = c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9'
		} else {
			word = unicode.IsLetter(c) || unicode.IsDigit(c) || unicode.IsMark(c)
		}
		if !word {
			gap = true
			continue
		}
		if gap && b.Len() > 0 {
			b.WriteByte(' ')
		}
		gap = false
		b.WriteRune(c)
	}
	return b.String()
}

// fold turns look-alike letters and leetspeak digits into plain letters, so "1gn0re" and a
// Cyrillic "o" inside "ignore" read as "ignore". Word rules run on the plain view and on this one.
// ponytail: a short table, not a full confusables list.
func fold(c rune) rune {
	switch c {
	case '0', 0x043e, 0x03bf:
		return 'o'
	case '1', 0x0456, 0x03b9:
		return 'i'
	case '3', 0x0435, 0x03b5:
		return 'e'
	case '4', '@', 0x0430, 0x03b1:
		return 'a'
	case '5', '$', 0x0455:
		return 's'
	case '7':
		return 't'
	case 0x0440, 0x03c1:
		return 'p'
	case 0x0441:
		return 'c'
	case 0x0443:
		return 'y'
	case 0x0445:
		return 'x'
	case 0x03bd:
		return 'v'
	}
	if c >= 0xff41 && c <= 0xff5a { // full-width Latin
		return 'a' + (c - 0xff41)
	}
	return c
}

// spacedLetters reports 13 or more single-letter words in a row, like "i g n o r e", a sign of hidden text.
func spacedLetters(s string) bool {
	run, n := 0, 0 // run: single-letter words in a row, n: characters in the current word
	letter := false
	for _, c := range s {
		var space bool
		if c < 0x80 { // ASCII fast path, the common case
			space = c == ' ' || c == '\n' || c == '\t' || c == '\r'
		} else {
			space = unicode.IsSpace(c)
		}
		if space {
			if n == 1 && letter {
				if run++; run >= 13 {
					return true
				}
			} else if n > 0 {
				run = 0
			}
			n, letter = 0, false
			continue
		}
		n++
		if c < 0x80 {
			letter = c|0x20 >= 'a' && c|0x20 <= 'z'
		} else {
			letter = unicode.IsLetter(c)
		}
	}
	return n == 1 && letter && run+1 >= 13
}

// b64Runs returns up to max runs of 24 or more base64 characters, with any trailing '='.
// A plain byte scan: the equivalent regexp was several times slower on long text.
func b64Runs(s string, max int) []string {
	var out []string
	start := -1
	for i := 0; i <= len(s) && len(out) < max; i++ {
		ok := i < len(s) && (s[i] >= 'a' && s[i] <= 'z' || s[i] >= 'A' && s[i] <= 'Z' || s[i] >= '0' && s[i] <= '9' || s[i] == '+' || s[i] == '/')
		switch {
		case ok && start < 0:
			start = i
		case !ok && start >= 0:
			end := i
			if end-start >= 24 {
				for end < len(s) && end-i < 2 && s[end] == '=' {
					end++
				}
				out = append(out, s[start:end])
			}
			start = -1
		}
	}
	return out
}

func decodeB64(tok string) []byte {
	tok = strings.TrimRight(tok, "=")
	if len(tok)%4 == 1 {
		tok = tok[:len(tok)-1]
	}
	b, err := base64.RawStdEncoding.DecodeString(tok)
	if err != nil {
		return nil
	}
	return b
}

func rot13(c rune) rune {
	if c >= 'a' && c <= 'z' {
		return 'a' + (c-'a'+13)%26
	}
	return c
}

func tokensScore(text string) (float64, string) { return scoreText(text, 0) }

// scoreText also scores text hidden in base64 or ROT13. depth stops that from nesting.
// ponytail: only the first 8 base64 runs, each under 64 KB, and ROT13 only when the text says "rot13".
func scoreText(text string, depth int) (float64, string) {
	lowered := strings.Map(func(c rune) rune {
		switch c {
		case 0x200b, 0x200c, 0x200d, 0x2060, 0xfeff:
			return -1 // zero-width characters hide phrases from naive matching
		}
		return c
	}, strings.ToLower(text))
	words := normWords(lowered)
	folded := words
	if mapped := strings.Map(fold, lowered); mapped != lowered {
		folded = normWords(mapped)
	}
	wordViews, rawViews := []string{words}, []string{lowered}
	if folded != words {
		wordViews = append(wordViews, folded)
	}

	miss, best, bestW := 1.0, "", 0.0
	hit := func(name string, w float64) {
		miss *= 1 - w // noisy-or: several weak signals add up
		if w > bestW {
			best, bestW = name, w
		}
	}
	var wordHits [][][][2]int // per word view, per rule: where its anchors sit
	if prefilter {
		for _, v := range wordViews {
			wordHits = append(wordHits, anchorHits(v))
		}
	}
	for ri, ru := range tokenRules {
		views := wordViews
		if ru.raw {
			views = rawViews
		}
		for vi, src := range views {
			var matched bool
			switch {
			case !prefilter:
				matched = ru.re.MatchString(src)
			case ru.raw:
				matched = containsAny(src, needs[ru.name]) && ru.re.MatchString(src)
			default:
				matched = matchWindows(ru.re, src, wordHits[vi][ri])
			}
			if matched {
				hit(ru.name, ru.w)
				break
			}
		}
	}
	if spacedLetters(lowered) {
		hit("spaced-letters", 0.5)
	}
	if depth == 0 {
		for _, tok := range b64Runs(text, 8) {
			if len(tok) > 1<<16 {
				continue
			}
			if dec := decodeB64(tok); dec != nil && utf8.Valid(dec) {
				if s, rule := scoreText(string(dec), 1); s >= 0.4 {
					hit("base64:"+rule, math.Min(s, 0.9)) // hiding a command in base64 is deliberate, so it counts for more
				}
			}
		}
		if strings.Contains(lowered, "rot13") {
			if s, rule := scoreText(strings.Map(rot13, lowered), 1); s >= 0.4 {
				hit("rot13:"+rule, math.Min(s, 0.9))
			}
		}
	}
	return 1 - miss, best
}
