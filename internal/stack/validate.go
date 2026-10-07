package stack

import (
	"fmt"
	"maps"
	"path"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/sebbi08/deployment-gateway/internal/names"
)

const (
	maxListItems   = 32
	maxArgBytes    = 4 << 10
	maxEnvEntries  = 100
	maxEnvValue    = 8 << 10
	maxEnvTotal    = 64 << 10
	maxPorts       = 10
	minMemoryBytes = 16 << 20
	minPids        = 16
)

var (
	envKeyRe  = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]{0,63}$`)
	userRe    = regexp.MustCompile(`^[0-9]{1,10}:[0-9]{1,10}$`)
	volPathRe = regexp.MustCompile(`^/[A-Za-z0-9._/-]*$`)
	imageRe   = regexp.MustCompile(`^(?:([a-zA-Z0-9][a-zA-Z0-9.-]*(?::[0-9]{1,5})?)/)?` +
		`([a-z0-9]+(?:(?:[._]|__|-+)[a-z0-9]+)*(?:/[a-z0-9]+(?:(?:[._]|__|-+)[a-z0-9]+)*)*)` +
		`(?::([A-Za-z0-9_][A-Za-z0-9_.-]{0,127}))?(?:@(sha256:[a-f0-9]{64}))?$`)
	forbiddenVolumePrefixes = []string{"/proc", "/sys", "/dev", "/tmp"}
)

// Limits are the per-workspace bounds a stack is validated against.
// PortRangeStart == 0 means the workspace may not publish ports.
type Limits struct {
	MaxServices    int
	MaxMemoryBytes int64
	MaxCPUs        float64
	MaxPids        int64
	PortRangeStart int
	PortRangeEnd   int
}

// Grants maps granted template names to whether more than one service of a
// stack may use them (true when the host pattern contains {{service}} or the
// template has no host pattern).
type Grants map[string]bool

// Validate checks every field of the stack and returns all problems found.
func Validate(s *Stack, lim Limits, grants Grants) Errors {
	var errs Errors
	if s == nil || len(s.Services) == 0 {
		errs.add("services", "at least one service is required")
		return errs
	}
	if len(s.Services) > lim.MaxServices {
		errs.add("services", "at most %d services allowed", lim.MaxServices)
	}
	hostPorts := map[string]string{}
	templateUse := map[string]int{}
	for _, name := range ServiceNames(s) {
		svc := s.Services[name]
		f := "services." + name
		if !names.ValidService(name) {
			errs.add(f, "service name must match ^[a-z][a-z0-9]{0,15}$")
			continue
		}
		validateImage(&errs, f+".image", svc.Image)
		validateArgs(&errs, f+".command", svc.Command)
		validateArgs(&errs, f+".entrypoint", svc.Entrypoint)
		validateEnv(&errs, f+".environment", svc.Environment)
		if e := svc.Expose; e != nil {
			if _, ok := grants[e.Template]; !ok {
				errs.add(f+".expose.template", "template %q is not granted to this workspace", e.Template)
			} else {
				templateUse[e.Template]++
			}
			if e.Port < 1 || e.Port > 65535 {
				errs.add(f+".expose.port", "must be 1-65535")
			}
		}
		validatePorts(&errs, f+".ports", svc.Ports, lim, hostPorts)
		if svc.Volume != nil {
			validateVolume(&errs, f+".volume.path", svc.Volume.Path)
		}
		if hc := svc.Healthcheck; hc != nil {
			if len(hc.Command) == 0 {
				errs.add(f+".healthcheck.command", "is required")
			}
			validateArgs(&errs, f+".healthcheck.command", hc.Command)
			if hc.Interval != "" {
				d, err := time.ParseDuration(hc.Interval)
				if err != nil || d < 5*time.Second || d > 5*time.Minute {
					errs.add(f+".healthcheck.interval", "must be a duration between 5s and 5m")
				}
			}
			if hc.Retries < 0 || hc.Retries > 10 {
				errs.add(f+".healthcheck.retries", "must be 1-10")
			}
		}
		for i, d := range svc.DependsOn {
			df := fmt.Sprintf("%s.depends_on[%d]", f, i)
			if d == name {
				errs.add(df, "a service cannot depend on itself")
			} else if _, ok := s.Services[d]; !ok {
				errs.add(df, "unknown service %q", d)
			}
		}
		if svc.Resources != nil {
			validateResources(&errs, f+".resources", *svc.Resources, lim)
		}
		if svc.User != "" && !validUser(svc.User) {
			errs.add(f+".user", "must be numeric uid:gid (each 0-4294967295), e.g. 999:999")
		}
	}
	for _, t := range slices.Sorted(maps.Keys(templateUse)) {
		if templateUse[t] > 1 && !grants[t] {
			errs.add("services", "template %q has no {{service}} in its host pattern; only one service per stack may use it", t)
		}
	}
	if c := findCycle(s); c != "" {
		errs.add("services", "depends_on cycle: %s", c)
	}
	return errs
}

func validateImage(errs *Errors, f, img string) {
	if img == "" {
		errs.add(f, "image is required")
		return
	}
	if len(img) > 255 {
		errs.add(f, "image reference too long")
		return
	}
	m := imageRe.FindStringSubmatch(img)
	if m == nil {
		errs.add(f, "invalid image reference")
		return
	}
	tag, digest := m[3], m[4]
	if tag == "" && digest == "" {
		errs.add(f, "image must have an explicit tag or digest (e.g. nginx:1.27.0)")
		return
	}
	if tag == "latest" {
		errs.add(f, `tag "latest" is not allowed; pin a version`)
	}
}

func validateArgs(errs *Errors, f string, args []string) {
	if len(args) > maxListItems {
		errs.add(f, "at most %d items allowed", maxListItems)
		return
	}
	for i, a := range args {
		if len(a) > maxArgBytes {
			errs.add(fmt.Sprintf("%s[%d]", f, i), "longer than %d bytes", maxArgBytes)
		}
		if strings.ContainsRune(a, 0) {
			errs.add(fmt.Sprintf("%s[%d]", f, i), "contains NUL byte")
		}
	}
}

func validateEnv(errs *Errors, f string, env map[string]string) {
	if len(env) > maxEnvEntries {
		errs.add(f, "at most %d variables allowed", maxEnvEntries)
	}
	total := 0
	for _, k := range slices.Sorted(maps.Keys(env)) {
		v := env[k]
		kf := f + "." + k
		total += len(k) + len(v)
		if !envKeyRe.MatchString(k) {
			errs.add(kf, "key must match ^[A-Za-z_][A-Za-z0-9_]{0,63}$")
		}
		if len(v) > maxEnvValue {
			errs.add(kf, "value longer than %d bytes", maxEnvValue)
		}
		if strings.ContainsRune(v, 0) {
			errs.add(kf, "value contains NUL byte")
		}
	}
	if total > maxEnvTotal {
		errs.add(f, "environment larger than %d bytes", maxEnvTotal)
	}
}

func validatePorts(errs *Errors, f string, ports []Port, lim Limits, seen map[string]string) {
	if len(ports) == 0 {
		return
	}
	if lim.PortRangeStart == 0 {
		errs.add(f, "this workspace may not publish ports")
		return
	}
	if len(ports) > maxPorts {
		errs.add(f, "at most %d ports per service", maxPorts)
	}
	for i, p := range ports {
		pf := fmt.Sprintf("%s[%d]", f, i)
		if p.Container < 1 || p.Container > 65535 {
			errs.add(pf+".container", "must be 1-65535")
		}
		if p.Host < lim.PortRangeStart || p.Host > lim.PortRangeEnd {
			errs.add(pf+".host", "must be within %d-%d", lim.PortRangeStart, lim.PortRangeEnd)
		}
		proto := Protocol(p)
		if proto != "tcp" && proto != "udp" {
			errs.add(pf+".protocol", "must be tcp or udp")
			continue
		}
		key := fmt.Sprintf("%s/%d", proto, p.Host)
		if prev, dup := seen[key]; dup {
			errs.add(pf+".host", "host port %s already used by %s", key, prev)
		} else {
			seen[key] = pf
		}
	}
}

func validateVolume(errs *Errors, f, p string) {
	switch {
	case !volPathRe.MatchString(p):
		errs.add(f, "must be an absolute path of letters, digits, . _ - /")
	case path.Clean(p) != p:
		errs.add(f, "must be normalized (no .., //, or trailing /)")
	case p == "/":
		errs.add(f, "must not be /")
	case strings.Contains(p, "docker.sock"):
		// The post-render verifier treats any docker.sock path as a
		// security violation; reject it here with a field error.
		errs.add(f, "must not contain docker.sock")
	default:
		for _, pre := range forbiddenVolumePrefixes {
			if p == pre || strings.HasPrefix(p, pre+"/") {
				errs.add(f, "must not be under %s", pre)
			}
		}
	}
}

func validateResources(errs *Errors, f string, r Resources, lim Limits) {
	if r.Memory != "" {
		b, err := ParseMemory(r.Memory)
		switch {
		case err != nil:
			errs.add(f+".memory", "%v", err)
		case b < minMemoryBytes:
			errs.add(f+".memory", "must be at least 16m")
		case b > lim.MaxMemoryBytes:
			errs.add(f+".memory", "exceeds per-service maximum of %dm", lim.MaxMemoryBytes>>20)
		}
	}
	if r.CPUs != 0 && (r.CPUs < 0.01 || r.CPUs > lim.MaxCPUs) {
		errs.add(f+".cpus", "must be between 0.01 and %g", lim.MaxCPUs)
	}
	if r.Pids != 0 && (r.Pids < minPids || r.Pids > lim.MaxPids) {
		errs.add(f+".pids", "must be between %d and %d", minPids, lim.MaxPids)
	}
}

// findCycle returns a human-readable cycle like "a -> b -> a", or "".
func findCycle(s *Stack) string {
	const (
		white = iota
		grey
		black
	)
	color := map[string]int{}
	var stackPath []string
	var visit func(n string) string
	visit = func(n string) string {
		color[n] = grey
		stackPath = append(stackPath, n)
		for _, d := range s.Services[n].DependsOn {
			if _, ok := s.Services[d]; !ok || d == n {
				continue
			}
			switch color[d] {
			case grey:
				i := slices.Index(stackPath, d)
				return strings.Join(append(slices.Clone(stackPath[i:]), d), " -> ")
			case white:
				if c := visit(d); c != "" {
					return c
				}
			}
		}
		stackPath = stackPath[:len(stackPath)-1]
		color[n] = black
		return ""
	}
	for _, n := range ServiceNames(s) {
		if color[n] == white {
			if c := visit(n); c != "" {
				return c
			}
		}
	}
	return ""
}

// validUser accepts "uid:gid" with both parts fitting in 32 bits.
func validUser(u string) bool {
	if !userRe.MatchString(u) {
		return false
	}
	uid, gid, _ := strings.Cut(u, ":")
	for _, n := range []string{uid, gid} {
		if _, err := strconv.ParseUint(n, 10, 32); err != nil {
			return false
		}
	}
	return true
}
