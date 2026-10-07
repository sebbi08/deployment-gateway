// Package tmpl implements admin-defined exposure templates (spec §8):
// networks + labels with placeholders + an optional host pattern whose
// structure guarantees globally unique hostnames.
package tmpl

import (
	"maps"
	"regexp"
	"slices"
	"strconv"
	"strings"

	"github.com/sebbi08/deployment-gateway/internal/names"
)

type Template struct {
	Name        string            `json:"name"`
	Description string            `json:"description"`
	HostPattern string            `json:"host_pattern,omitempty"`
	Networks    []string          `json:"networks"`
	Labels      map[string]string `json:"labels"`
}

type Vars struct {
	Workspace string
	App       string
	Service   string
	Port      int
}

type ValidationError []string

func (v ValidationError) Error() string { return strings.Join(v, "; ") }

var (
	placeholderRe = regexp.MustCompile(`\{\{[^{}]*\}\}`)
	hostRe        = regexp.MustCompile(`^((?:\{\{(?:workspace|app|service)\}\}[-.])*\{\{(?:workspace|app|service)\}\})\.([a-z0-9.-]+)$`)
	dnsLabelRe    = regexp.MustCompile(`^[a-z0-9](?:[a-z0-9-]{0,61}[a-z0-9])?$`)
	networkRe     = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9_.-]{0,62}$`)
	labelKeyRe    = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._/{}-]{0,255}$`)

	labelPlaceholders = map[string]bool{"workspace": true, "app": true, "service": true, "id": true, "host": true, "port": true}
	reservedNetworks  = map[string]bool{"host": true, "none": true, "bridge": true}
)

// Validate checks a template on save. others are the other stored
// templates; an entry with the same Name as t is ignored (edit case).
func Validate(t Template, others []Template) error {
	var errs ValidationError
	add := func(s string) { errs = append(errs, s) }
	if !names.ValidTemplate(t.Name) {
		add("name must match ^[a-z][a-z0-9-]{1,30}$")
	}
	if len(t.Description) > 500 {
		add("description longer than 500 characters")
	}
	if t.HostPattern != "" {
		validateHost(t, add)
		if suffix := HostSuffix(t.HostPattern); suffix != "" {
			for _, o := range others {
				if o.Name == t.Name || o.HostPattern == "" {
					continue
				}
				if os := HostSuffix(o.HostPattern); os != "" && suffixesOverlap(suffix, os) {
					add("host suffix " + strconv.Quote(suffix) + " overlaps suffix " + strconv.Quote(os) + " of template " + strconv.Quote(o.Name))
				}
			}
		}
	}
	for _, n := range t.Networks {
		switch {
		case !networkRe.MatchString(n):
			add("invalid network name " + strconv.Quote(n))
		case reservedNetworks[n]:
			add("network " + strconv.Quote(n) + " is not allowed")
		case strings.HasPrefix(n, "dg-"):
			add("network " + strconv.Quote(n) + ": the dg- prefix is reserved for per-app networks")
		}
	}
	for _, k := range slices.Sorted(maps.Keys(t.Labels)) {
		v := t.Labels[k]
		if strings.HasPrefix(k, "{{") {
			add("label key " + strconv.Quote(k) + " must not start with a placeholder")
		} else if !labelKeyRe.MatchString(k) {
			add("invalid label key " + strconv.Quote(k))
		}
		if mayHavePrefix(k, names.LabelPrefix) {
			add("label key " + strconv.Quote(k) + ": the deploy-gateway. prefix is reserved")
		}
		if mayHavePrefix(k, "com.docker.compose.") {
			add("label key " + strconv.Quote(k) + ": the com.docker.compose. prefix is reserved for compose")
		}
		if len(v) > 4096 {
			add("label " + strconv.Quote(k) + ": value longer than 4096 bytes")
		}
		if strings.ContainsAny(v, "\x00\r\n") {
			add("label " + strconv.Quote(k) + ": value must not contain NUL or newline")
		}
		checkPlaceholders("label "+strconv.Quote(k), k+v, add)
		if strings.Contains(k+v, "{{host}}") && t.HostPattern == "" {
			add("label " + strconv.Quote(k) + " uses {{host}} but the template has no host_pattern")
		}
	}
	checkKeyCollisions(t, add)
	checkTraefikNames(t, add)
	if len(errs) > 0 {
		return errs
	}
	return nil
}

// mayHavePrefix reports whether label key k can render to a key starting
// with prefix: it does already, or a placeholder follows a literal part that
// prefix starts with (deploy-{{app}}. becomes deploy-gateway. for app
// "gateway").
func mayHavePrefix(k, prefix string) bool {
	k = strings.ToLower(k)
	if strings.HasPrefix(k, prefix) {
		return true
	}
	lit, _, hasPh := strings.Cut(k, "{{")
	return hasPh && strings.HasPrefix(prefix, lit)
}

// traefikObjectRe matches Traefik router and service labels and captures the
// object name.
var traefikObjectRe = regexp.MustCompile(`(?i)^traefik\.(?:http|tcp|udp)\.(?:routers|services)\.([^.]+)\.`)

// checkTraefikNames requires {{id}} in every Traefik router and service
// name. Traefik's docker provider shares one namespace across all
// containers: names built from {{app}} or literals collide between
// workspaces (and with the host's own stacks), and Traefik then drops or
// merges the routers, which breaks or hijacks another tenant's routing.
// Middleware definitions may be shared (identical definitions are fine).
func checkTraefikNames(t Template, add func(string)) {
	for _, k := range slices.Sorted(maps.Keys(t.Labels)) {
		if m := traefikObjectRe.FindStringSubmatch(k); m != nil && !strings.Contains(m[1], "{{id}}") {
			add("label " + strconv.Quote(k) + ": Traefik router and service names must contain {{id}} so they are unique per deployed service")
		}
	}
}

// checkKeyCollisions rejects label keys that can render to the same key
// (compared case-insensitively, as Traefik reads them): RenderLabels would
// then keep a random one of the values. Two sample sets are used: distinct
// names, and identical names (an app and its service may share a name).
func checkKeyCollisions(t Template, add func(string)) {
	samples := []Vars{
		{Workspace: "wsone", App: "appone", Service: "svcone", Port: 8080},
		{Workspace: "x", App: "x", Service: "x", Port: 8080},
	}
	reported := map[string]bool{}
	for _, v := range samples {
		r := t.replacer(v)
		seen := map[string]string{}
		for _, k := range slices.Sorted(maps.Keys(t.Labels)) {
			rk := strings.ToLower(r.Replace(k))
			if first, ok := seen[rk]; ok {
				if msg := "label keys " + strconv.Quote(first) + " and " + strconv.Quote(k) + " can render to the same label key"; !reported[msg] {
					reported[msg] = true
					add(msg)
				}
				continue
			}
			seen[rk] = k
		}
	}
}

func validateHost(t Template, add func(string)) {
	m := hostRe.FindStringSubmatch(t.HostPattern)
	if m == nil {
		add("host_pattern must be <placeholders separated by - or .>.<lowercase domain>, e.g. {{app}}.{{workspace}}.apps.example.com")
		return
	}
	sub, suffix := m[1], m[2]
	counts := map[string]int{}
	for _, ph := range placeholderRe.FindAllString(sub, -1) {
		counts[ph]++
	}
	for _, ph := range slices.Sorted(maps.Keys(counts)) {
		if counts[ph] > 1 {
			add("host_pattern uses " + ph + " more than once")
		}
	}
	if counts["{{workspace}}"] == 0 || counts["{{app}}"] == 0 {
		add("host_pattern must contain {{workspace}} and {{app}}")
	}
	if !validDomain(suffix) || !strings.Contains(suffix, ".") {
		add("host suffix " + strconv.Quote(suffix) + " must be a valid domain with at least two labels")
		return
	}
	longest := t.Host(Vars{Workspace: strings.Repeat("w", 16), App: strings.Repeat("a", 24), Service: strings.Repeat("s", 16)})
	if len(longest) > 253 || !validDomain(longest) {
		add("longest possible host " + strconv.Quote(longest) + " is not a valid DNS name")
	}
}

func checkPlaceholders(what, s string, add func(string)) {
	for _, ph := range placeholderRe.FindAllString(s, -1) {
		if !labelPlaceholders[strings.Trim(ph, "{}")] {
			add(what + ": unknown placeholder " + ph)
		}
	}
	rest := placeholderRe.ReplaceAllString(s, "")
	if strings.Contains(rest, "{{") || strings.Contains(rest, "}}") {
		add(what + ": malformed placeholder")
	}
}

func validDomain(s string) bool {
	if s == "" {
		return false
	}
	for _, l := range strings.Split(s, ".") {
		if !dnsLabelRe.MatchString(l) {
			return false
		}
	}
	return true
}

func suffixesOverlap(a, b string) bool {
	return a == b || strings.HasSuffix(a, "."+b) || strings.HasSuffix(b, "."+a)
}

// HostSuffix returns the literal domain part of a host pattern, or "".
func HostSuffix(pattern string) string {
	m := hostRe.FindStringSubmatch(pattern)
	if m == nil {
		return ""
	}
	return m[2]
}

// Host renders the host pattern; "" if the template has none.
func (t Template) Host(v Vars) string {
	if t.HostPattern == "" {
		return ""
	}
	return strings.NewReplacer("{{workspace}}", v.Workspace, "{{app}}", v.App, "{{service}}", v.Service).Replace(t.HostPattern)
}

func (t Template) replacer(v Vars) *strings.Replacer {
	return strings.NewReplacer(
		"{{workspace}}", v.Workspace,
		"{{app}}", v.App,
		"{{service}}", v.Service,
		"{{id}}", names.Container(v.Workspace, v.App, v.Service),
		"{{host}}", t.Host(v),
		"{{port}}", strconv.Itoa(v.Port),
	)
}

// RenderLabels substitutes placeholders in label keys and values.
func (t Template) RenderLabels(v Vars) map[string]string {
	r := t.replacer(v)
	out := make(map[string]string, len(t.Labels))
	for k, val := range t.Labels {
		out[r.Replace(k)] = r.Replace(val)
	}
	return out
}

// Shareable reports whether more than one service of a stack may use the
// template without producing duplicate hosts.
func (t Template) Shareable() bool {
	return t.HostPattern == "" || strings.Contains(t.HostPattern, "{{service}}")
}

// LabelPrefixes returns the lower-cased label namespaces used by templates:
// the first dot-segment of every label key plus ".". A key whose first
// segment contains a placeholder reserves its literal part before the
// placeholder; a key without a dot reserves itself as a namespace. Images
// and containers must not carry labels in these namespaces unless the
// gateway rendered them.
func LabelPrefixes(ts []Template) []string {
	set := map[string]bool{}
	for _, t := range ts {
		for k := range t.Labels {
			k = strings.ToLower(k)
			seg, _, _ := strings.Cut(k, ".")
			if lit, _, hasPh := strings.Cut(seg, "{{"); hasPh {
				if lit != "" {
					set[lit] = true
				}
				continue
			}
			if seg != "" {
				set[seg+"."] = true
			}
		}
	}
	return slices.Sorted(maps.Keys(set))
}
