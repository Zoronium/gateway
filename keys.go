// Virtual keys: separate keys for separate apps or people, each with its own model list and
// requests-per-minute limit. The secret is shown once; only its SHA-256 hash is stored.
// The master GATEWAY_KEY stays the admin key and is never stored here.
package main

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"regexp"
	"strings"
	"sync"
	"time"
)

type keyRec struct {
	ID, Name, Prefix string
	Created          time.Time
	Revoked          bool
	Models           []string // allowed models: "groq/*", "ollama/qwen2.5:0.5b", "*". Empty means all.
	RPM              int      // requests per minute, 0 means no limit
}

type bucket struct {
	tokens float64
	last   time.Time
}

type keyring struct {
	st      *store
	mu      sync.Mutex
	byHash  map[string]*keyRec // active keys only
	buckets map[string]*bucket
}

func newKeyring(st *store) *keyring {
	return &keyring{st: st, byHash: map[string]*keyRec{}, buckets: map[string]*bucket{}}
}

func hashKey(secret string) string {
	sum := sha256.Sum256([]byte(secret))
	return hex.EncodeToString(sum[:])
}

// load fills the cache from the database, so every request checks a key without touching disk.
func (k *keyring) load() error {
	recs, hashes, err := k.st.allKeys()
	if err != nil {
		return err
	}
	k.mu.Lock()
	defer k.mu.Unlock()
	for i := range recs {
		if !recs[i].Revoked {
			r := recs[i]
			k.byHash[hashes[i]] = &r
		}
	}
	return nil
}

// lookup returns the active key for a secret. The map is keyed by hash, so the secret itself is never compared.
func (k *keyring) lookup(secret string) (*keyRec, bool) {
	k.mu.Lock()
	defer k.mu.Unlock()
	r, ok := k.byHash[hashKey(secret)]
	return r, ok
}

var modelPattern = regexp.MustCompile(`^[A-Za-z0-9._:/@*-]{1,200}$`)

// create makes a key and returns its secret. This is the only time the secret exists in clear.
func (k *keyring) create(name string, models []string, rpm int) (keyRec, string, error) {
	name = strings.TrimSpace(name)
	if name == "" || len(name) > 64 {
		return keyRec{}, "", fmt.Errorf("name must be 1 to 64 characters")
	}
	if rpm < 0 || rpm > 1_000_000 {
		return keyRec{}, "", fmt.Errorf("rpm must be between 0 and 1000000 (0 means no limit)")
	}
	var clean []string
	for _, m := range models {
		if m = strings.TrimSpace(m); m == "" {
			continue
		}
		if !modelPattern.MatchString(m) {
			return keyRec{}, "", fmt.Errorf("bad model pattern %q", m)
		}
		clean = append(clean, m)
	}
	b := make([]byte, 24)
	rand.Read(b)
	secret := "sk-gw-" + hex.EncodeToString(b)
	idb := make([]byte, 4)
	rand.Read(idb)
	rec := keyRec{ID: "key_" + hex.EncodeToString(idb), Name: name, Prefix: secret[:10], Created: time.Now().UTC().Truncate(time.Second), Models: clean, RPM: rpm}
	if err := k.st.insertKey(rec, hashKey(secret)); err != nil {
		return keyRec{}, "", err
	}
	k.mu.Lock()
	r := rec
	k.byHash[hashKey(secret)] = &r
	k.mu.Unlock()
	return rec, secret, nil
}

// revoke stops a key from working at once. It reports false if there was no active key with that id.
func (k *keyring) revoke(id string) (bool, error) {
	ok, err := k.st.setRevoked(id)
	if err != nil || !ok {
		return false, err
	}
	k.mu.Lock()
	defer k.mu.Unlock()
	for h, r := range k.byHash {
		if r.ID == id {
			delete(k.byHash, h)
		}
	}
	delete(k.buckets, id)
	return true, nil
}

func (k *keyring) list() ([]keyRec, error) {
	recs, _, err := k.st.allKeys()
	return recs, err
}

// allows reports whether the key may use provider/model. An empty list allows everything.
func (r *keyRec) allows(provider, model string) bool {
	if len(r.Models) == 0 {
		return true
	}
	full := provider + "/" + model
	for _, p := range r.Models {
		if p == "*" || p == full || (strings.HasSuffix(p, "*") && strings.HasPrefix(full, strings.TrimSuffix(p, "*"))) {
			return true
		}
	}
	return false
}

// allow takes one request from the key's bucket. The bucket holds up to rpm requests and refills at
// rpm per minute. When empty it says how long until the next request fits.
// ponytail: in memory, so limits reset on restart and are per process. Move to the database if you run several copies.
func (k *keyring) allow(id string, rpm int) (bool, time.Duration) {
	if rpm <= 0 {
		return true, 0
	}
	now := time.Now()
	k.mu.Lock()
	defer k.mu.Unlock()
	b := k.buckets[id]
	if b == nil {
		b = &bucket{tokens: float64(rpm), last: now}
		k.buckets[id] = b
	}
	rate := float64(rpm) / 60 // tokens per second
	b.tokens = min(float64(rpm), b.tokens+now.Sub(b.last).Seconds()*rate)
	b.last = now
	if b.tokens >= 1 {
		b.tokens--
		return true, 0
	}
	return false, time.Duration((1 - b.tokens) / rate * float64(time.Second))
}
