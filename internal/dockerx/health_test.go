package dockerx

import "testing"

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
