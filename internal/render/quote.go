package render

import (
	"fmt"
	"maps"
	"slices"
	"strings"

	"go.yaml.in/yaml/v3"
)

// qstr is an agent- or template-supplied string. It is always emitted as a
// double-quoted scalar: yaml.v3 otherwise picks block styles that break or
// silently alter strings with leading newlines, spaces or tabs, and emits
// "<<" as a merge key.
type qstr string

func (s qstr) MarshalYAML() (any, error) {
	return &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Style: yaml.DoubleQuotedStyle, Value: string(s)}, nil
}

func qstrs(in []string) []qstr {
	if in == nil {
		return nil
	}
	out := make([]qstr, len(in))
	for i, s := range in {
		out[i] = qstr(s)
	}
	return out
}

func qmap(in map[string]string) map[string]qstr {
	if in == nil {
		return nil
	}
	out := make(map[string]qstr, len(in))
	for k, v := range in {
		out[k] = qstr(v)
	}
	return out
}

// FieldError reports a rendered value that does not survive a YAML round
// trip; the field uses the agent's names (services.<svc>.<field>).
type FieldError struct {
	Field   string
	Message string
}

func (e *FieldError) Error() string { return e.Field + ": " + e.Message }

// roundTrip parses the rendered YAML again and checks that every agent and
// template string reads back exactly as rendered.
func roundTrip(y []byte, f composeFile) error {
	var doc struct {
		Services map[string]struct {
			Command     []string          `yaml:"command"`
			Entrypoint  []string          `yaml:"entrypoint"`
			Environment map[string]string `yaml:"environment"`
			Labels      map[string]string `yaml:"labels"`
			Healthcheck *struct {
				Test []string `yaml:"test"`
			} `yaml:"healthcheck"`
		} `yaml:"services"`
	}
	if err := yaml.Unmarshal(y, &doc); err != nil {
		return fmt.Errorf("render: rendered YAML does not parse: %w", err)
	}
	for _, id := range slices.Sorted(maps.Keys(f.Services)) {
		want, got := f.Services[id], doc.Services[id]
		svc := id[strings.LastIndex(id, "-")+1:]
		field := func(name string) *FieldError {
			return &FieldError{Field: "services." + svc + "." + name, Message: "value cannot be represented exactly in the rendered configuration"}
		}
		if !slices.Equal(strs(want.Command), got.Command) {
			return field("command")
		}
		if !slices.Equal(strs(want.Entrypoint), got.Entrypoint) {
			return field("entrypoint")
		}
		for _, k := range slices.Sorted(maps.Keys(want.Environment)) {
			if v, ok := got.Environment[k]; !ok || v != string(want.Environment[k]) {
				return field("environment." + k)
			}
		}
		for _, k := range slices.Sorted(maps.Keys(want.Labels)) {
			if v, ok := got.Labels[k]; !ok || v != string(want.Labels[k]) {
				return field("labels." + k)
			}
		}
		if want.Healthcheck != nil && (got.Healthcheck == nil || !slices.Equal(strs(want.Healthcheck.Test), got.Healthcheck.Test)) {
			return field("healthcheck.command")
		}
	}
	return nil
}

func strs(in []qstr) []string {
	if in == nil {
		return nil
	}
	out := make([]string, len(in))
	for i, s := range in {
		out[i] = string(s)
	}
	return out
}
