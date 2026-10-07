package dockerx

import (
	"fmt"
	"strings"
	"testing"
)

func TestHealthFromStatus(t *testing.T) {
	cases := map[string]string{
		"Up 5 minutes (healthy)":          "healthy",
		"Up 2 seconds (health: starting)": "starting",
		"Up 1 hour (unhealthy)":           "unhealthy",
		"Up 3 minutes":                    "",
		"Exited (1) 2 minutes ago":        "",
		"Up 10 seconds (Paused)":          "",
		"":                                "",
	}
	for status, want := range cases {
		if got := healthFromStatus(status); got != want {
			t.Errorf("healthFromStatus(%q) = %q, want %q", status, got, want)
		}
	}
}

// Logs keep the newest lines: the decisive error is usually at the end.
func TestTailWriterKeepsNewestLines(t *testing.T) {
	w := &tailWriter{max: 20}
	for i := range 10 {
		fmt.Fprintf(w, "line %d\n", i)
	}
	got := w.String()
	if !strings.HasSuffix(got, "line 8\nline 9\n") || strings.Contains(got, "line 0") || len(got) > 20+len(truncatedMark) {
		t.Fatalf("tail = %q", got)
	}
	if !strings.HasPrefix(got, truncatedMark) {
		t.Fatalf("truncation not marked: %q", got)
	}
	small := &tailWriter{max: 100}
	small.Write([]byte("a\nb\n"))
	if small.String() != "a\nb\n" {
		t.Fatalf("untruncated output changed: %q", small.String())
	}
}

// Docker 29+ reports "none" for containers without a healthcheck, older
// daemons nothing; both mean "no health state".
func TestNormalizeHealth(t *testing.T) {
	for in, want := range map[string]string{"none": "", "": "", "healthy": "healthy", "starting": "starting", "unhealthy": "unhealthy"} {
		if got := normalizeHealth(in); got != want {
			t.Errorf("normalizeHealth(%q) = %q, want %q", in, got, want)
		}
	}
}
