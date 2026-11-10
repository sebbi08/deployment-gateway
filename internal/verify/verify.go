// Package verify independently checks a rendered compose file against the
// security invariants (spec §10, layer 2). It deliberately shares no code
// with package render: a renderer bug must not be able to hide from it.
package verify

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"maps"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"

	"go.yaml.in/yaml/v4"

	"github.com/sebbi08/deployment-gateway/internal/names"
)

type Policy struct {
	Workspace      string
	App            string
	DataDir        string
	Networks       map[string]bool
	MaxMemoryBytes int64
	MaxCPUs        float64
	MaxPids        int64
	PortRangeStart int
	PortRangeEnd   int
	BindIP         string
}

type Violations []string

func (v Violations) Error() string { return "compose verification failed: " + strings.Join(v, "; ") }

func (v *Violations) addf(format string, args ...any) { *v = append(*v, fmt.Sprintf(format, args...)) }

var (
	allowedServiceKeys = set("image", "container_name", "hostname", "command", "entrypoint", "environment",
		"user", "restart", "cap_drop", "security_opt", "read_only", "tmpfs", "mem_limit", "memswap_limit",
		"cpus", "pids_limit", "logging", "volumes", "ports", "healthcheck", "depends_on", "networks", "labels")
	forbiddenKeys = set("privileged", "cap_add", "devices", "device_cgroup_rules", "network_mode", "pid", "ipc",
		"uts", "userns_mode", "cgroup", "cgroup_parent", "runtime", "extra_hosts", "build", "extends", "include",
		"configs", "secrets", "sysctls", "ulimits", "volumes_from", "links", "external_links", "storage_opt",
		"isolation", "gpus")
	allowedTags = set("", "!!str", "!!int", "!!float", "!!bool", "!!map", "!!seq", "!!null")
	ownerLabels = set(names.LabelWorkspace, names.LabelApp, names.LabelService, names.LabelManaged)
	userRe      = regexp.MustCompile(`^[0-9]{1,10}:[0-9]{1,10}$`)
)

func set(items ...string) map[string]bool {
	m := make(map[string]bool, len(items))
	for _, i := range items {
		m[i] = true
	}
	return m
}

func Verify(doc []byte, p Policy) error {
	var v Violations
	var root yaml.Node
	dec := yaml.NewDecoder(bytes.NewReader(doc))
	if err := dec.Decode(&root); err != nil {
		return Violations{"unparseable YAML: " + err.Error()}
	}
	var extra yaml.Node
	if err := dec.Decode(&extra); !errors.Is(err, io.EOF) {
		return Violations{"multiple YAML documents not allowed"}
	}
	checkNodes(&root, &v)
	if len(v) > 0 {
		return v
	}
	var f map[string]any
	if err := root.Decode(&f); err != nil {
		return Violations{"decode: " + err.Error()}
	}
	for _, k := range sortedKeys(f) {
		if k != "name" && k != "services" && k != "networks" {
			v.addf("top-level key %q not allowed", k)
		}
	}
	if s, _ := f["name"].(string); s != names.Project(p.Workspace, p.App) {
		v.addf("name must be %q", names.Project(p.Workspace, p.App))
	}
	checkTopNetworks(f["networks"], p, &v)
	services, ok := f["services"].(map[string]any)
	if !ok || len(services) == 0 {
		v.addf("services missing")
	}
	for _, key := range sortedKeys(services) {
		checkService(key, services[key], services, p, &v)
	}
	checkDollar(f, "", &v)
	if len(v) > 0 {
		return v
	}
	return nil
}

func checkNodes(n *yaml.Node, v *Violations) {
	if n.Kind == yaml.AliasNode {
		v.addf("YAML alias at line %d not allowed", n.Line)
		return
	}
	if n.Anchor != "" {
		v.addf("YAML anchor %q at line %d not allowed", n.Anchor, n.Line)
	}
	if !allowedTags[n.Tag] {
		v.addf("YAML tag %q at line %d not allowed", n.Tag, n.Line)
	}
	for _, c := range n.Content {
		checkNodes(c, v)
	}
}

func checkTopNetworks(raw any, p Policy, v *Violations) {
	m, ok := raw.(map[string]any)
	own := names.Network(p.Workspace, p.App)
	if !ok {
		v.addf("top-level networks missing")
		return
	}
	if _, ok := m[own]; !ok {
		v.addf("top-level network %q missing", own)
	}
	for _, k := range sortedKeys(m) {
		n, ok := m[k].(map[string]any)
		if !ok {
			v.addf("network %q must be a mapping", k)
			continue
		}
		for _, nk := range sortedKeys(n) {
			if nk != "name" && nk != "external" {
				v.addf("network %q: key %q not allowed", k, nk)
			}
		}
		if n["name"] != k {
			v.addf("network %q: name must equal its key", k)
		}
		ext, _ := n["external"].(bool)
		switch {
		case k == own:
			if ext {
				v.addf("network %q must not be external", k)
			}
		case !p.Networks[k]:
			v.addf("network %q not allowed", k)
		case !ext:
			v.addf("template network %q must be external", k)
		}
	}
}

func checkService(key string, raw any, all map[string]any, p Policy, v *Violations) {
	s, ok := raw.(map[string]any)
	if !ok {
		v.addf("service %q is not a mapping", key)
		return
	}
	ws, app, svc, ok := names.Parse(key)
	if !ok || ws != p.Workspace || app != p.App {
		v.addf("service key %q is not <workspace>-<app>-<service> for this app", key)
		return
	}
	for _, k := range sortedKeys(s) {
		if forbiddenKeys[k] {
			v.addf("%s: forbidden key %q", key, k)
		} else if !allowedServiceKeys[k] {
			v.addf("%s: key %q not allowed", key, k)
		}
	}
	if img, _ := s["image"].(string); img == "" {
		v.addf("%s: image missing", key)
	}
	if s["container_name"] != key {
		v.addf("%s: container_name must be %q", key, key)
	}
	if s["hostname"] != key {
		v.addf("%s: hostname must be %q", key, key)
	}
	if s["restart"] != "unless-stopped" {
		v.addf("%s: restart must be unless-stopped", key)
	}
	if !equalStrings(s["cap_drop"], "ALL") {
		v.addf("%s: cap_drop must be [ALL]", key)
	}
	if !equalStrings(s["security_opt"], "no-new-privileges:true") {
		v.addf("%s: security_opt must be exactly [no-new-privileges:true]", key)
	}
	if _, ok := s["read_only"].(bool); !ok {
		v.addf("%s: read_only must be set", key)
	}
	if !equalStrings(s["tmpfs"], "/tmp") {
		v.addf("%s: tmpfs must be [/tmp]", key)
	}
	mem, okMem := asInt(s["mem_limit"])
	if !okMem || mem <= 0 || mem > p.MaxMemoryBytes {
		v.addf("%s: mem_limit missing or above %d", key, p.MaxMemoryBytes)
	}
	if swap, ok := asInt(s["memswap_limit"]); !ok || swap != mem {
		v.addf("%s: memswap_limit must equal mem_limit", key)
	}
	if cpus, ok := asFloat(s["cpus"]); !ok || cpus <= 0 || cpus > p.MaxCPUs {
		v.addf("%s: cpus missing or above %g", key, p.MaxCPUs)
	}
	if pids, ok := asInt(s["pids_limit"]); !ok || pids <= 0 || pids > p.MaxPids {
		v.addf("%s: pids_limit missing or above %d", key, p.MaxPids)
	}
	if u, present := s["user"]; present {
		if us, _ := u.(string); !userRe.MatchString(us) {
			v.addf("%s: user must be numeric uid:gid", key)
		}
	}
	checkLogging(key, s["logging"], v)
	checkVolumes(key, svc, s["volumes"], p, v)
	checkPorts(key, s["ports"], p, v)
	checkServiceNetworks(key, s["networks"], p, v)
	checkLabels(key, svc, s["labels"], p, v)
	checkDependsOn(key, s["depends_on"], all, v)
	checkHealth(key, s["healthcheck"], v)
}

func checkLogging(key string, raw any, v *Violations) {
	m, ok := raw.(map[string]any)
	if !ok || m["driver"] != "json-file" {
		v.addf("%s: logging driver must be json-file", key)
		return
	}
	opts, _ := m["options"].(map[string]any)
	for _, k := range sortedKeys(opts) {
		if k != "max-size" && k != "max-file" {
			v.addf("%s: logging option %q not allowed", key, k)
		}
	}
	// The rotation limits bound what a container can write to the host disk.
	if opts["max-size"] != "10m" || opts["max-file"] != "3" {
		v.addf("%s: logging options must be max-size 10m and max-file 3", key)
	}
	for _, k := range sortedKeys(m) {
		if k != "driver" && k != "options" {
			v.addf("%s: logging key %q not allowed", key, k)
		}
	}
}

func checkVolumes(key, svc string, raw any, p Policy, v *Violations) {
	if raw == nil {
		return
	}
	list, ok := raw.([]any)
	if !ok {
		v.addf("%s: volumes must be a list", key)
		return
	}
	if len(list) > 1 {
		v.addf("%s: at most one volume allowed", key)
	}
	want := filepath.Join(p.DataDir, svc)
	for _, item := range list {
		m, ok := item.(map[string]any)
		if !ok {
			v.addf("%s: volumes must use long syntax", key)
			continue
		}
		for _, k := range sortedKeys(m) {
			if k != "type" && k != "source" && k != "target" && k != "bind" {
				v.addf("%s: volume key %q not allowed", key, k)
			}
		}
		src, _ := m["source"].(string)
		tgt, _ := m["target"].(string)
		if strings.Contains(src, "docker.sock") || strings.Contains(tgt, "docker.sock") {
			v.addf("%s: docker.sock must never be mounted", key)
		}
		if m["type"] != "bind" {
			v.addf(`%s: volume type must be "bind"`, key)
			continue
		}
		if src != want {
			v.addf("%s: bind source %q must be %q", key, src, want)
			continue
		}
		if !path.IsAbs(tgt) {
			v.addf("%s: volume target must be absolute", key)
		}
		if b, present := m["bind"]; present {
			bm, ok := b.(map[string]any)
			if !ok || len(bm) != 1 || bm["create_host_path"] != false {
				v.addf("%s: bind options must be exactly {create_host_path: false}", key)
			}
		}
		checkNoSymlink(key, src, v)
	}
}

// checkNoSymlink resolves the deepest existing ancestor of src and requires
// that it resolves to itself (no symlink anywhere on the path).
func checkNoSymlink(key, src string, v *Violations) {
	existing := src
	for {
		if _, err := os.Lstat(existing); err == nil {
			break
		}
		parent := filepath.Dir(existing)
		if parent == existing {
			return
		}
		existing = parent
	}
	real, err := filepath.EvalSymlinks(existing)
	if err != nil || real != existing {
		v.addf("%s: bind path %q contains a symlink", key, existing)
	}
}

func checkPorts(key string, raw any, p Policy, v *Violations) {
	if raw == nil {
		return
	}
	list, ok := raw.([]any)
	if !ok {
		v.addf("%s: ports must be a list", key)
		return
	}
	if p.PortRangeStart == 0 && len(list) > 0 {
		v.addf("%s: ports not allowed for this workspace", key)
		return
	}
	for _, item := range list {
		m, ok := item.(map[string]any)
		if !ok {
			v.addf("%s: ports must use long syntax", key)
			continue
		}
		for _, k := range sortedKeys(m) {
			if k != "target" && k != "published" && k != "host_ip" && k != "protocol" {
				v.addf("%s: port key %q not allowed", key, k)
			}
		}
		if t, ok := asInt(m["target"]); !ok || t < 1 || t > 65535 {
			v.addf("%s: port target invalid", key)
		}
		pub, ok := asPort(m["published"])
		if !ok || pub < p.PortRangeStart || pub > p.PortRangeEnd {
			v.addf("%s: published port %v outside %d-%d", key, m["published"], p.PortRangeStart, p.PortRangeEnd)
		}
		if m["host_ip"] != p.BindIP {
			v.addf("%s: host_ip must be %q", key, p.BindIP)
		}
		if m["protocol"] != "tcp" && m["protocol"] != "udp" {
			v.addf("%s: protocol must be tcp or udp", key)
		}
	}
}

func checkServiceNetworks(key string, raw any, p Policy, v *Violations) {
	list, ok := raw.([]any)
	if !ok {
		v.addf("%s: networks must be a plain list (aliases not allowed)", key)
		return
	}
	own := names.Network(p.Workspace, p.App)
	hasOwn := false
	for _, item := range list {
		n, _ := item.(string)
		if n == own {
			hasOwn = true
		}
		if !p.Networks[n] {
			v.addf("%s: network %q not allowed", key, n)
		}
	}
	if !hasOwn {
		v.addf("%s: must join %q", key, own)
	}
}

func checkLabels(key, svc string, raw any, p Policy, v *Violations) {
	m, ok := raw.(map[string]any)
	if !ok {
		v.addf("%s: labels missing", key)
		return
	}
	want := map[string]string{
		names.LabelWorkspace: p.Workspace,
		names.LabelApp:       p.App,
		names.LabelService:   svc,
		names.LabelManaged:   "true",
	}
	for _, k := range sortedKeys(m) {
		if _, ok := m[k].(string); !ok {
			v.addf("%s: label %q must be a string", key, k)
		}
		if strings.HasPrefix(strings.ToLower(k), names.LabelPrefix) && !ownerLabels[k] {
			v.addf("%s: label %q not allowed", key, k)
		}
	}
	for _, k := range slices.Sorted(maps.Keys(want)) {
		if m[k] != want[k] {
			v.addf("%s: label %s must be %q", key, k, want[k])
		}
	}
}

func checkDependsOn(key string, raw any, all map[string]any, v *Violations) {
	if raw == nil {
		return
	}
	m, ok := raw.(map[string]any)
	if !ok {
		v.addf("%s: depends_on must be a mapping", key)
		return
	}
	for _, dep := range sortedKeys(m) {
		if _, ok := all[dep]; !ok {
			v.addf("%s: depends_on unknown service %q", key, dep)
		}
		d, ok := m[dep].(map[string]any)
		if !ok || len(d) != 1 || (d["condition"] != "service_started" && d["condition"] != "service_healthy") {
			v.addf("%s: depends_on %q must only set condition", key, dep)
		}
	}
}

func checkHealth(key string, raw any, v *Violations) {
	if raw == nil {
		return
	}
	m, ok := raw.(map[string]any)
	if !ok {
		v.addf("%s: healthcheck must be a mapping", key)
		return
	}
	for _, k := range sortedKeys(m) {
		switch k {
		case "test", "interval", "retries", "start_period", "start_interval":
		default:
			v.addf("%s: healthcheck key %q not allowed", key, k)
		}
	}
	test, _ := m["test"].([]any)
	if len(test) < 2 || test[0] != "CMD" {
		v.addf("%s: healthcheck test must be exec form [CMD, ...]", key)
	}
}

// checkDollar finds any $ not written as $$ in string values.
func checkDollar(node any, at string, v *Violations) {
	switch n := node.(type) {
	case string:
		if strings.Contains(strings.ReplaceAll(n, "$$", ""), "$") {
			v.addf("%s: unescaped $ in value", at)
		}
	case map[string]any:
		for _, k := range sortedKeys(n) {
			checkDollar(n[k], at+"."+k, v)
		}
	case []any:
		for i, item := range n {
			checkDollar(item, at+"["+strconv.Itoa(i)+"]", v)
		}
	}
}

func equalStrings(raw any, want ...string) bool {
	list, ok := raw.([]any)
	if !ok || len(list) != len(want) {
		return false
	}
	for i, item := range list {
		if item != want[i] {
			return false
		}
	}
	return true
}

func asInt(raw any) (int64, bool) {
	switch n := raw.(type) {
	case int:
		return int64(n), true
	case int64:
		return n, true
	case uint64:
		if n > 1<<62 {
			return 0, false
		}
		return int64(n), true
	}
	return 0, false
}

func asFloat(raw any) (float64, bool) {
	switch n := raw.(type) {
	case float64:
		return n, true
	case int:
		return float64(n), true
	}
	return 0, false
}

func asPort(raw any) (int, bool) {
	switch n := raw.(type) {
	case string:
		i, err := strconv.Atoi(n)
		return i, err == nil
	case int:
		return n, true
	}
	return 0, false
}

func sortedKeys(m map[string]any) []string { return slices.Sorted(maps.Keys(m)) }
