package dockerx

import "strings"

// healthFromStatus extracts the health state from a container status line
// such as "Up 5 minutes (healthy)". Daemons before API 1.52 (Docker 28 and
// older) do not fill Summary.Health in container lists, but always include
// the health state in the status text.
func healthFromStatus(status string) string {
	switch {
	case strings.Contains(status, "(healthy)"):
		return "healthy"
	case strings.Contains(status, "(unhealthy)"):
		return "unhealthy"
	case strings.Contains(status, "(health: starting)"):
		return "starting"
	}
	return ""
}
