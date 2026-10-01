// Package stack defines the agent-supplied stack format: a strict, JSON-only
// subset of compose (spec §7). Nothing in here is ever passed to Docker
// directly; the render package turns a validated Stack into compose YAML.
package stack

import (
	"maps"
	"slices"
)

type Stack struct {
	Services map[string]Service `json:"services" jsonschema:"services of this app keyed by short service name (^[a-z][a-z0-9]{0,15}$)"`
}

type Service struct {
	Image          string            `json:"image" jsonschema:"image reference with explicit tag or digest; :latest is rejected"`
	Command        []string          `json:"command,omitempty" jsonschema:"exec-form command"`
	Entrypoint     []string          `json:"entrypoint,omitempty" jsonschema:"exec-form entrypoint"`
	Environment    map[string]string `json:"environment,omitempty" jsonschema:"environment variables; values are passed literally (no interpolation)"`
	Expose         *Expose           `json:"expose,omitempty" jsonschema:"make this service reachable through an admin-defined exposure template"`
	Ports          []Port            `json:"ports,omitempty" jsonschema:"publish container ports on host ports inside the workspace port range"`
	Volume         *Volume           `json:"volume,omitempty" jsonschema:"persistent directory inside the workspace folder, mounted at path"`
	Healthcheck    *Healthcheck      `json:"healthcheck,omitempty"`
	DependsOn      []string          `json:"depends_on,omitempty" jsonschema:"short service names in this stack to start first"`
	Resources      *Resources        `json:"resources,omitempty"`
	User           string            `json:"user,omitempty" jsonschema:"numeric uid:gid, e.g. 999:999; required for images whose entrypoint drops privileges"`
	WritableRootfs bool              `json:"writable_rootfs,omitempty" jsonschema:"disable the read-only root filesystem"`
}

type Expose struct {
	Template string `json:"template" jsonschema:"name of a granted exposure template (see list_templates)"`
	Port     int    `json:"port" jsonschema:"container port the template routes to"`
}

type Port struct {
	Container int    `json:"container"`
	Host      int    `json:"host"`
	Protocol  string `json:"protocol,omitempty" jsonschema:"tcp (default) or udp"`
}

type Volume struct {
	Path string `json:"path" jsonschema:"absolute container path"`
}

type Healthcheck struct {
	Command  []string `json:"command" jsonschema:"exec-form command run inside the container"`
	Interval string   `json:"interval,omitempty" jsonschema:"Go duration 5s..5m, default 30s"`
	Retries  int      `json:"retries,omitempty" jsonschema:"1..10, default 3"`
}

type Resources struct {
	Memory string  `json:"memory,omitempty" jsonschema:"e.g. 256m or 1g"`
	CPUs   float64 `json:"cpus,omitempty"`
	Pids   int64   `json:"pids,omitempty"`
}

// ServiceNames returns the service names in sorted order so that rendering,
// validation messages and results are deterministic.
func ServiceNames(s *Stack) []string {
	return slices.Sorted(maps.Keys(s.Services))
}
