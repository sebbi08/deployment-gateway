package auth

import (
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
	trusted := []netip.Prefix{netip.MustParsePrefix("172.19.0.149/32")}
	r := httptest.NewRequest("GET", "/", nil)
	r.RemoteAddr = "172.19.0.149:5555"
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
	r.RemoteAddr = "172.19.0.149:1"
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
