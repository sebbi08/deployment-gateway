package service

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// With format: raw a newline in a value would start another variable, so
// writeEnvFiles refuses such values and writes nothing at all.
func TestWriteEnvFilesRefusesLineBreaks(t *testing.T) {
	for name, bad := range map[string]string{"LF": "a\nINJECTED=1", "CR": "a\rb", "NUL": "a\x00b"} {
		t.Run(name, func(t *testing.T) {
			s := New(nil, nil, nil, t.TempDir())
			dir := s.secretsDir("alice", "shop")
			if err := os.MkdirAll(dir, 0o700); err != nil {
				t.Fatal(err)
			}
			old := filepath.Join(dir, "web.env")
			if err := os.WriteFile(old, []byte("K=old\n"), 0o600); err != nil {
				t.Fatal(err)
			}
			pl := &planned{secretValues: map[string]map[string]string{
				"api": {"OK": "fine-value"},
				"web": {"K": bad},
			}}
			err := s.writeEnvFiles("alice", "shop", pl)
			if err == nil {
				t.Fatal("expected an error")
			}
			if strings.Contains(err.Error(), bad) {
				t.Fatalf("error leaks the value: %q", err)
			}
			if data, _ := os.ReadFile(old); string(data) != "K=old\n" {
				t.Fatalf("web.env changed: %q", data)
			}
			if entries, _ := os.ReadDir(dir); len(entries) != 1 {
				t.Fatalf("files written: %v", entries)
			}
		})
	}
}

func TestMaskValues(t *testing.T) {
	mask := maskValues([]string{"abcdef", "abcdef123", "abc12", ""})
	if got := mask("x abcdef123 y abcdef z abc12"); got != "x *** y *** z abc12" {
		t.Fatalf("mask = %q", got)
	}
	if got := maskValues(nil)("unchanged abcdef"); got != "unchanged abcdef" {
		t.Fatalf("mask without values = %q", got)
	}
}
