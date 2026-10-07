package auth

import (
	"encoding/base64"
	"fmt"
	"net/http/httptest"
	"net/netip"
	"strings"
	"testing"
	"time"
)

func TestGenerateToken(t *testing.T) {
	tok, hash, prefix, err := GenerateToken()
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(tok, "dgw_") || len(tok) != 47 {
		t.Fatalf("token %q has wrong shape", tok)
	}
	if hash != HashToken(tok) || len(hash) != 64 || prefix != tok[:12] {
		t.Fatalf("hash/prefix mismatch: %q %q", hash, prefix)
	}
	tok2, _, _, _ := GenerateToken()
	if tok == tok2 {
		t.Fatal("tokens must be random")
	}
}

func TestPassword(t *testing.T) {
	h, err := HashPassword("correct horse")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(h, "$argon2id$v=19$m=65536,t=3,p=2$") {
		t.Fatalf("unexpected encoding %q", h)
	}
	if !CheckPassword(h, "correct horse") || CheckPassword(h, "wrong") {
		t.Fatal("password check broken")
	}
	for _, bad := range []string{"", "plain", "$argon2id$v=19$m=999999999,t=3,p=2$c2FsdA$aGFzaA", "$argon2i$v=19$m=65536,t=3,p=2$c2FsdA$aGFzaA"} {
		if CheckPassword(bad, "x") {
			t.Errorf("malformed hash %q accepted", bad)
		}
	}
}

func TestLimiter(t *testing.T) {
	now := time.Unix(0, 0)
	l := NewLimiter(3, time.Minute, 15*time.Minute)
	l.Now = func() time.Time { return now }
	for i := 0; i < 2; i++ {
		l.Fail("1.2.3.4")
	}
	if l.Blocked("1.2.3.4") {
		t.Fatal("blocked too early")
	}
	now = now.Add(2 * time.Minute) // old failures expire
	l.Fail("1.2.3.4")
	if l.Blocked("1.2.3.4") {
		t.Fatal("expired failures counted")
	}
	l.Fail("1.2.3.4")
	l.Fail("1.2.3.4")
	if !l.Blocked("1.2.3.4") || l.Blocked("5.6.7.8") {
		t.Fatal("block state wrong")
	}
	now = now.Add(16 * time.Minute)
	if l.Blocked("1.2.3.4") {
		t.Fatal("block did not expire")
	}
}

func TestClientIP(t *testing.T) {
	trusted := []netip.Prefix{netip.MustParsePrefix("172.20.0.10/32")}
	r := httptest.NewRequest("GET", "/", nil)
	r.RemoteAddr = "172.20.0.10:5555"
	r.Header.Set("X-Forwarded-For", "9.9.9.9, 203.0.113.7")
	if got := ClientIP(r, trusted); got != "203.0.113.7" {
		t.Fatalf("trusted proxy: got %q", got)
	}
	r.RemoteAddr = "10.0.0.5:5555"
	if got := ClientIP(r, trusted); got != "10.0.0.5" {
		t.Fatalf("untrusted peer must ignore XFF: got %q", got)
	}
	if PeerTrusted(r, trusted) {
		t.Fatal("peer should not be trusted")
	}
	r.RemoteAddr = "172.20.0.10:1"
	if !PeerTrusted(r, trusted) {
		t.Fatal("peer should be trusted")
	}
}

func TestSessions(t *testing.T) {
	now := time.Unix(0, 0)
	s := NewSessions(time.Hour)
	s.Now = func() time.Time { return now }
	id, err := s.Create("admin")
	if err != nil {
		t.Fatal(err)
	}
	if actor, ok := s.Lookup(id); !ok || actor != "admin" {
		t.Fatal("lookup failed")
	}
	now = now.Add(59 * time.Minute)
	if _, ok := s.Lookup(id); !ok {
		t.Fatal("lookup must refresh idle timer")
	}
	now = now.Add(61 * time.Minute)
	if _, ok := s.Lookup(id); ok {
		t.Fatal("session should have expired")
	}
	id, _ = s.Create("admin")
	s.Delete(id)
	if _, ok := s.Lookup(id); ok {
		t.Fatal("deleted session still valid")
	}
}

// A proxy that appends its own X-Forwarded-For line (instead of extending
// the client's) must not let the client choose its address; entries with a
// port are accepted.
func TestClientIPUsesAllForwardedForLines(t *testing.T) {
	trusted := []netip.Prefix{netip.MustParsePrefix("10.0.0.1/32")}
	r := httptest.NewRequest("GET", "/", nil)
	r.RemoteAddr = "10.0.0.1:1234"
	r.Header.Add("X-Forwarded-For", "6.6.6.6")     // client-supplied
	r.Header.Add("X-Forwarded-For", "203.0.113.9") // appended by the proxy
	if got := ClientIP(r, trusted); got != "203.0.113.9" {
		t.Fatalf("two XFF lines: got %s, want the proxy-appended address", got)
	}
	r.Header.Del("X-Forwarded-For")
	r.Header.Set("X-Forwarded-For", "203.0.113.9:5555")
	if got := ClientIP(r, trusted); got != "203.0.113.9" {
		t.Fatalf("ip:port entry: got %s", got)
	}
	r.Header.Set("X-Forwarded-For", "[2001:db8::7]:443")
	if got := ClientIP(r, trusted); got != "2001:db8::7" {
		t.Fatalf("[v6]:port entry: got %s", got)
	}
}

func TestLimiterSweepsExpiredBlocks(t *testing.T) {
	l := NewLimiter(1, time.Minute, time.Minute)
	now := time.Unix(1_000_000, 0)
	l.Now = func() time.Time { return now }
	for i := range 3000 {
		l.Fail(fmt.Sprintf("198.51.%d.%d", i/256, i%256))
	}
	now = now.Add(2 * time.Minute)
	l.Fail("203.0.113.1")
	if n := l.BlockedCount(); n > 1 {
		t.Fatalf("%d blocks kept after expiry", n)
	}
}

// Rotating addresses inside one IPv6 /64 counts as one client.
func TestLimiterKeysIPv6By64(t *testing.T) {
	l := NewLimiter(3, time.Minute, time.Minute)
	for i := range 3 {
		l.Fail(fmt.Sprintf("2001:db8:1:2::%x", i+1))
	}
	if !l.Blocked("2001:db8:1:2::ffff") {
		t.Fatal("addresses of one /64 must share a block")
	}
	if l.Blocked("2001:db8:1:3::1") || l.Blocked("192.0.2.1") {
		t.Fatal("other prefixes must not be blocked")
	}
}

// Session IDs are bearer credentials: unique and 256 bits long.
func TestSessionIDsAreUniqueAndLong(t *testing.T) {
	s := NewSessions(time.Hour)
	seen := map[string]bool{}
	for range 1000 {
		id, err := s.Create("admin")
		if err != nil {
			t.Fatal(err)
		}
		if len(id) < 43 {
			t.Fatalf("session id %q is shorter than 256 bits", id)
		}
		if seen[id] {
			t.Fatalf("session id %q issued twice", id)
		}
		seen[id] = true
	}
}

// The hash covers the whole token, not just the displayed prefix: tokens
// that share the prefix must hash differently.
func TestHashTokenCoversWholeToken(t *testing.T) {
	tok, _, prefix, err := GenerateToken()
	if err != nil {
		t.Fatal(err)
	}
	other := prefix + strings.Repeat("A", len(tok)-len(prefix))
	if other == tok {
		other = prefix + strings.Repeat("B", len(tok)-len(prefix))
	}
	if HashToken(tok) == HashToken(other) {
		t.Fatal("tokens with the same prefix hash alike")
	}
	last := tok[:len(tok)-1] + "x"
	if tok[len(tok)-1] == 'x' {
		last = tok[:len(tok)-1] + "y"
	}
	if HashToken(tok) == HashToken(last) {
		t.Fatal("the last token character does not change the hash")
	}
}

// A block lasts the whole block duration, not just the instant it starts.
func TestLimiterBlockLastsFullDuration(t *testing.T) {
	now := time.Unix(1_000_000, 0)
	l := NewLimiter(3, time.Minute, 15*time.Minute)
	l.Now = func() time.Time { return now }
	for range 3 {
		l.Fail("192.0.2.1")
	}
	start := now
	now = start.Add(15*time.Minute - time.Second)
	if !l.Blocked("192.0.2.1") {
		t.Fatal("block ended before the block duration")
	}
	now = start.Add(15 * time.Minute)
	if l.Blocked("192.0.2.1") {
		t.Fatal("block outlived the block duration")
	}
}

// Failures spread across the window all count, not only those at the same
// instant.
func TestLimiterCountsFailuresAcrossWindow(t *testing.T) {
	start := time.Unix(1_000_000, 0)
	now := start
	l := NewLimiter(3, time.Minute, 15*time.Minute)
	l.Now = func() time.Time { return now }
	for _, at := range []time.Duration{0, 25 * time.Second, 50 * time.Second} {
		now = start.Add(at)
		l.Fail("192.0.2.1")
	}
	if !l.Blocked("192.0.2.1") {
		t.Fatal("three failures within one window must block")
	}
}

// Failure bookkeeping is bounded: many distinct clients that never reach
// the limit cannot grow the map without bound.
func TestLimiterCapsTrackedFailures(t *testing.T) {
	l := NewLimiter(3, time.Hour, time.Hour)
	for i := range 10002 {
		l.Fail(fmt.Sprintf("198.%d.%d.%d", i>>16&255, i>>8&255, i&255))
	}
	if n := len(l.fails); n > 10001 {
		t.Fatalf("%d failure keys kept, want the map capped at 10000", n)
	}
}

// Behind a chain of trusted proxies the first untrusted address from the
// right is the client; an unparseable entry ends the walk at the peer, and
// IPv4-mapped IPv6 entries match IPv4 prefixes.
func TestClientIPWalksTrustedHops(t *testing.T) {
	trusted := []netip.Prefix{netip.MustParsePrefix("172.20.0.0/16")}
	r := httptest.NewRequest("GET", "/", nil)
	r.RemoteAddr = "172.20.0.10:5555"
	for _, c := range []struct{ xff, want string }{
		{"203.0.113.7, 172.20.0.5", "203.0.113.7"},
		{"6.6.6.6, unknown", "172.20.0.10"},
		{"6.6.6.6, unknown, 172.20.0.5", "172.20.0.10"},
		{"203.0.113.7, ::ffff:172.20.0.5", "203.0.113.7"},
	} {
		r.Header.Set("X-Forwarded-For", c.xff)
		if got := ClientIP(r, trusted); got != c.want {
			t.Errorf("XFF %q: got %s, want %s", c.xff, got, c.want)
		}
	}
}

// Each parameter check in parseHash rejects on its own: every case differs
// from a valid hash in one field only, and the range bounds are inclusive.
func TestParseHashChecksEachField(t *testing.T) {
	salt := base64.RawStdEncoding.EncodeToString(make([]byte, 16))
	key := base64.RawStdEncoding.EncodeToString(make([]byte, 32))
	enc := func(alg, ver, params, salt, key string) string {
		return "$" + alg + "$" + ver + "$" + params + "$" + salt + "$" + key
	}
	b64 := func(n int) string { return base64.RawStdEncoding.EncodeToString(make([]byte, n)) }
	for _, ok := range []string{
		enc("argon2id", "v=19", "m=65536,t=3,p=2", salt, key),
		enc("argon2id", "v=19", "m=8192,t=1,p=1", b64(8), b64(16)),
		enc("argon2id", "v=19", "m=1048576,t=10,p=16", salt, b64(64)),
	} {
		if err := ValidPasswordHash(ok); err != nil {
			t.Errorf("valid hash %q rejected: %v", ok, err)
		}
	}
	for name, bad := range map[string]string{
		"argon2i":   enc("argon2i", "v=19", "m=65536,t=3,p=2", salt, key),
		"version":   enc("argon2id", "v=16", "m=65536,t=3,p=2", salt, key),
		"m too big": enc("argon2id", "v=19", "m=1048577,t=3,p=2", salt, key),
		"m too low": enc("argon2id", "v=19", "m=8191,t=3,p=2", salt, key),
		"t too big": enc("argon2id", "v=19", "m=65536,t=11,p=2", salt, key),
		"t zero":    enc("argon2id", "v=19", "m=65536,t=0,p=2", salt, key),
		"p too big": enc("argon2id", "v=19", "m=65536,t=3,p=17", salt, key),
		"p zero":    enc("argon2id", "v=19", "m=65536,t=3,p=0", salt, key),
		"salt":      enc("argon2id", "v=19", "m=65536,t=3,p=2", b64(7), key),
		"key short": enc("argon2id", "v=19", "m=65536,t=3,p=2", salt, b64(15)),
		"key long":  enc("argon2id", "v=19", "m=65536,t=3,p=2", salt, b64(65)),
	} {
		if ValidPasswordHash(bad) == nil {
			t.Errorf("%s: %q accepted", name, bad)
		}
	}
}

// Every hash gets a fresh random salt: hashing one password twice yields
// different hashes, and neither salt is all zeros.
func TestHashPasswordUsesRandomSalt(t *testing.T) {
	h1, err := HashPassword("correct horse")
	if err != nil {
		t.Fatal(err)
	}
	h2, err := HashPassword("correct horse")
	if err != nil {
		t.Fatal(err)
	}
	if h1 == h2 {
		t.Fatal("two hashes of one password are identical")
	}
	zero := base64.RawStdEncoding.EncodeToString(make([]byte, 16))
	if strings.Split(h1, "$")[4] == zero || strings.Split(h2, "$")[4] == zero {
		t.Fatal("salt is all zeros")
	}
}
