package names

import (
	"strings"
	"testing"
)

func TestValidators(t *testing.T) {
	cases := []struct {
		kind string
		fn   func(string) bool
		in   string
		want bool
	}{
		{"ws", ValidWorkspace, "alice", true},
		{"ws", ValidWorkspace, "a1", true},
		{"ws", ValidWorkspace, "a", false},
		{"ws", ValidWorkspace, "al-ice", false},
		{"ws", ValidWorkspace, "Alice", false},
		{"ws", ValidWorkspace, "abcdefghijklmnop", true},
		{"ws", ValidWorkspace, "abcdefghijklmnopq", false},
		{"app", ValidApp, "shop", true},
		{"app", ValidApp, "my-shop", true},
		{"app", ValidApp, "s", false},
		{"app", ValidApp, "shop-", false},
		{"app", ValidApp, "-shop", false},
		{"app", ValidApp, "abcdefghijklmnopqrstuvwx", true},
		{"app", ValidApp, "abcdefghijklmnopqrstuvwxy", false},
		{"svc", ValidService, "web", true},
		{"svc", ValidService, "w", true},
		{"svc", ValidService, "my-web", false},
		{"svc", ValidService, "abcdefghijklmnopq", false},
		// No dots or underscores anywhere: compose rejects dotted project
		// names, and host uniqueness (spec §8) assumes dot-free names.
		{"ws", ValidWorkspace, "al.ice", false},
		{"ws", ValidWorkspace, "al_ice", false},
		{"app", ValidApp, "my.shop", false},
		{"app", ValidApp, "my_shop", false},
		{"svc", ValidService, "w.b", false},
		{"svc", ValidService, "w_b", false},
		{"tpl", ValidTemplate, "public-web", true},
		{"tpl", ValidTemplate, "p", false},
	}
	for _, c := range cases {
		if got := c.fn(c.in); got != c.want {
			t.Errorf("%s %q: got %v want %v", c.kind, c.in, got, c.want)
		}
	}
}

func TestComposedNames(t *testing.T) {
	if got := Container("alice", "my-shop", "db"); got != "alice-my-shop-db" {
		t.Fatalf("Container = %q", got)
	}
	if got := Project("alice", "shop"); got != "dg-alice-shop" {
		t.Fatalf("Project = %q", got)
	}
	if got := Network("alice", "shop"); got != "dg-alice-shop" {
		t.Fatalf("Network = %q", got)
	}
	longest := Container(strings.Repeat("w", 16), strings.Repeat("a", 24), strings.Repeat("s", 16))
	if len(longest) != 58 {
		t.Fatalf("longest name has %d chars, want 58 (< 63 DNS label limit)", len(longest))
	}
}

func TestParseRoundTrip(t *testing.T) {
	ws, app, svc, ok := Parse("alice-my-shop-db")
	if !ok || ws != "alice" || app != "my-shop" || svc != "db" {
		t.Fatalf("Parse = %q %q %q %v", ws, app, svc, ok)
	}
	for _, bad := range []string{"alice", "alice-db", "Alice-shop-db", "alice-shop-my-db-", "alice--db"} {
		if _, _, _, ok := Parse(bad); ok {
			t.Errorf("Parse(%q) should fail", bad)
		}
	}
}

func TestComposedNamesNeverCollide(t *testing.T) {
	wss := []string{"ab", "abc", "a1"}
	apps := []string{"x1", "x1-ab", "ab-x1", "x1-x1", "ab"}
	svcs := []string{"ab", "x1", "abc"}
	seen := map[string]string{}
	for _, w := range wss {
		for _, a := range apps {
			for _, s := range svcs {
				n := Container(w, a, s)
				triple := w + "|" + a + "|" + s
				if prev, dup := seen[n]; dup {
					t.Fatalf("collision: %s and %s both produce %s", prev, triple, n)
				}
				seen[n] = triple
			}
		}
	}
}
