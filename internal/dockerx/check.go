package dockerx

import (
	"fmt"
	"maps"
	"path/filepath"
	"regexp"
	"slices"
	"strings"

	"github.com/sebmahr/deploy-gateway/internal/names"
)

type MountFact struct {
	Type        string
	Name        string
	Source      string
	Destination string
}

type PortFact struct {
	ContainerPort int
	Protocol      string
	HostIP        string
	HostPort      int
}

// Facts is the security-relevant subset of a container inspect result.
type Facts struct {
	Name         string
	Labels       map[string]string
	Privileged   bool
	CapAdd       []string
	CapDrop      []string
	SecurityOpt  []string
	NetworkMode  string
	PidMode      string
	IpcMode      string
	UTSMode      string
	UsernsMode   string
	Runtime      string
	Devices      int
	Memory       int64
	PidsLimit    int64
	Mounts       []MountFact
	PortBindings []PortFact
	State        string
	Health       string
}

// RuntimePolicy describes what one container of an app may look like.
// Ports are the expected bindings for this container's service.
type RuntimePolicy struct {
	Workspace string
	App       string
	DataDir   string
	Ports     []PortFact
	// Labels are the labels the gateway rendered for this service.
	Labels map[string]string
	// ReservedPrefixes are label namespaces (e.g. "traefik.") in which the
	// container may carry only exactly the rendered labels. Image LABELs
	// are merged into container labels by Docker, so this catches labels
	// smuggled in through the image.
	ReservedPrefixes []string
}

// Anonymous volumes (from an image's VOLUME instruction) have random 64-hex
// names and live in Docker's own storage; they grant no host access.
var anonymousVolumeRe = regexp.MustCompile(`^[a-f0-9]{64}$`)

// CheckRuntime verifies a running container's real configuration (spec §10
// layer 3) and returns human-readable violations.
func CheckRuntime(f Facts, p RuntimePolicy) []string {
	var v []string
	add := func(format string, args ...any) { v = append(v, f.Name+": "+fmt.Sprintf(format, args...)) }
	if f.Privileged {
		add("privileged")
	}
	if len(f.CapAdd) > 0 {
		add("cap_add %v", f.CapAdd)
	}
	if !slices.ContainsFunc(f.CapDrop, func(c string) bool { return strings.EqualFold(c, "ALL") }) {
		add("cap_drop does not contain ALL")
	}
	if !slices.Contains(f.SecurityOpt, "no-new-privileges:true") && !slices.Contains(f.SecurityOpt, "no-new-privileges") {
		add("no-new-privileges not set")
	}
	for _, m := range []struct{ name, mode string }{
		{"network", f.NetworkMode}, {"pid", f.PidMode}, {"ipc", f.IpcMode}, {"uts", f.UTSMode}, {"userns", f.UsernsMode},
	} {
		if m.mode == "host" || strings.HasPrefix(m.mode, "container:") {
			add("%s mode %q", m.name, m.mode)
		}
	}
	if f.Runtime != "" && f.Runtime != "runc" {
		add("runtime %q", f.Runtime)
	}
	if f.Devices > 0 {
		add("devices mapped")
	}
	if f.Memory <= 0 {
		add("no memory limit")
	}
	if f.PidsLimit <= 0 {
		add("no pids limit")
	}
	prefix := filepath.Clean(p.DataDir) + string(filepath.Separator)
	for _, m := range f.Mounts {
		switch m.Type {
		case "bind":
			if !strings.HasPrefix(filepath.Clean(m.Source)+string(filepath.Separator), prefix) || filepath.Clean(m.Source) == filepath.Clean(p.DataDir) {
				add("bind mount %q outside %s", m.Source, prefix)
			}
		case "volume":
			if !anonymousVolumeRe.MatchString(m.Name) {
				add("named volume %q", m.Name)
			}
		case "tmpfs":
		default:
			add("mount type %q", m.Type)
		}
	}
	if f.Labels[names.LabelWorkspace] != p.Workspace || f.Labels[names.LabelApp] != p.App {
		add("owner labels do not match %s/%s", p.Workspace, p.App)
	}
	for _, k := range slices.Sorted(maps.Keys(f.Labels)) {
		if !HasReservedPrefix(k, p.ReservedPrefixes) {
			continue
		}
		if want, ok := p.Labels[k]; !ok || want != f.Labels[k] {
			add("label %q=%q was not rendered by the gateway", k, f.Labels[k])
		}
	}
	if !samePorts(f.PortBindings, p.Ports) {
		add("port bindings %v do not match expected %v", f.PortBindings, p.Ports)
	}
	return v
}

// HasReservedPrefix reports whether key (case-insensitively) lies in one of
// the reserved label namespaces. A prefix "x." also matches the bare key "x".
func HasReservedPrefix(key string, prefixes []string) bool {
	k := strings.ToLower(key)
	for _, p := range prefixes {
		p = strings.ToLower(p)
		if strings.HasPrefix(k, p) || (strings.HasSuffix(p, ".") && k == strings.TrimSuffix(p, ".")) {
			return true
		}
	}
	return false
}

func samePorts(a, b []PortFact) bool {
	key := func(p PortFact) string {
		return fmt.Sprintf("%d/%s@%s:%d", p.ContainerPort, p.Protocol, p.HostIP, p.HostPort)
	}
	ka, kb := make([]string, len(a)), make([]string, len(b))
	for i, p := range a {
		ka[i] = key(p)
	}
	for i, p := range b {
		kb[i] = key(p)
	}
	slices.Sort(ka)
	slices.Sort(kb)
	return slices.Equal(ka, kb)
}
