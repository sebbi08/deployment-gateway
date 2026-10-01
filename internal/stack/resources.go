package stack

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

var memRe = regexp.MustCompile(`^([1-9][0-9]{0,6})([kmg])$`)

// ParseMemory parses sizes like 512k, 256m, 1g (case-insensitive).
func ParseMemory(s string) (int64, error) {
	m := memRe.FindStringSubmatch(strings.ToLower(s))
	if m == nil {
		return 0, fmt.Errorf("memory must look like 256m or 1g")
	}
	n, _ := strconv.ParseInt(m[1], 10, 64)
	switch m[2] {
	case "k":
		return n << 10, nil
	case "m":
		return n << 20, nil
	default:
		return n << 30, nil
	}
}

type Defaults struct {
	MemoryBytes int64
	CPUs        float64
	Pids        int64
}

type Resolved struct {
	MemoryBytes int64
	CPUs        float64
	Pids        int64
}

// Effective resolves a validated service's limits, falling back to defaults.
func Effective(svc Service, d Defaults) Resolved {
	r := Resolved{MemoryBytes: d.MemoryBytes, CPUs: d.CPUs, Pids: d.Pids}
	if svc.Resources != nil {
		if b, err := ParseMemory(svc.Resources.Memory); err == nil {
			r.MemoryBytes = b
		}
		if svc.Resources.CPUs > 0 {
			r.CPUs = svc.Resources.CPUs
		}
		if svc.Resources.Pids > 0 {
			r.Pids = svc.Resources.Pids
		}
	}
	return r
}

// Totals sums effective memory and CPUs over all services.
func Totals(s *Stack, d Defaults) (mem int64, cpus float64) {
	for _, svc := range s.Services {
		r := Effective(svc, d)
		mem += r.MemoryBytes
		cpus += r.CPUs
	}
	return mem, cpus
}

// Protocol returns the port protocol with the tcp default applied.
func Protocol(p Port) string {
	if p.Protocol == "" {
		return "tcp"
	}
	return p.Protocol
}
