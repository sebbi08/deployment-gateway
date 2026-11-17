package service

import (
	"strings"
	"testing"
)

func TestRedactMasksTopLevelValue(t *testing.T) {
	got := redact(map[string]any{"name": "x", "value": "secret"})
	if strings.Contains(got, "secret") || !strings.Contains(got, `"value":"***"`) || !strings.Contains(got, `"name":"x"`) {
		t.Fatalf("redact = %s", got)
	}
}
