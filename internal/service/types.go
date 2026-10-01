package service

import (
	"github.com/sebmahr/deploy-gateway/internal/stack"
	"github.com/sebmahr/deploy-gateway/internal/store"
)

type NoInput struct{}

type DeployInput struct {
	App   string      `json:"app" jsonschema:"app name, ^[a-z][a-z0-9-]{0,22}[a-z0-9]$"`
	Stack stack.Stack `json:"stack" jsonschema:"strict compose subset describing the app's services"`
}

type ServiceInput struct {
	App     string `json:"app" jsonschema:"app name"`
	Service string `json:"service,omitempty" jsonschema:"short service name; empty means all services"`
}

type LogsInput struct {
	App     string `json:"app" jsonschema:"app name"`
	Service string `json:"service" jsonschema:"short service name"`
	Tail    int    `json:"tail,omitempty" jsonschema:"number of lines, default 200, max 1000"`
	Since   string `json:"since,omitempty" jsonschema:"Go duration like 10m or an RFC3339 timestamp"`
}

type DeleteInput struct {
	App     string `json:"app" jsonschema:"app name"`
	Confirm string `json:"confirm" jsonschema:"must equal the app name"`
}

type Usage struct {
	Apps     int     `json:"apps"`
	MemoryMB int64   `json:"memory_mb"`
	CPUs     float64 `json:"cpus"`
}

type WhoamiResult struct {
	Workspace  string            `json:"workspace"`
	Quota      store.Quota       `json:"quota"`
	Usage      Usage             `json:"usage"`
	PortRange  string            `json:"port_range" jsonschema:"host port range for published ports, empty if publishing is disabled"`
	PortBindIP string            `json:"port_bind_ip"`
	PortsInUse []store.PortClaim `json:"ports_in_use"`
	Naming     string            `json:"naming"`
	Guidance   []string          `json:"guidance"`
}

type TemplateView struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	HostPattern string `json:"host_pattern"`
	Shareable   bool   `json:"shareable" jsonschema:"true if several services of one app may use this template"`
}

type TemplatesResult struct {
	Templates []TemplateView `json:"templates"`
}

type ServicePlan struct {
	Service      string            `json:"service"`
	InternalHost string            `json:"internal_host"`
	URL          string            `json:"url,omitempty"`
	Ports        []store.PortClaim `json:"ports"`
	MemoryMB     int64             `json:"memory_mb"`
	CPUs         float64           `json:"cpus"`
}

type ValidateResult struct {
	App           string        `json:"app"`
	Services      []ServicePlan `json:"services"`
	TotalMemoryMB int64         `json:"total_memory_mb"`
	TotalCPUs     float64       `json:"total_cpus"`
}

type ServiceStatus struct {
	Service      string            `json:"service"`
	InternalHost string            `json:"internal_host"`
	URL          string            `json:"url,omitempty"`
	State        string            `json:"state"`
	Health       string            `json:"health,omitempty"`
	Ports        []store.PortClaim `json:"ports"`
}

type AppStatus struct {
	App      string          `json:"app"`
	Services []ServiceStatus `json:"services"`
}

type AppView struct {
	App              string          `json:"app"`
	Services         []ServiceStatus `json:"services"`
	DiskBytes        int64           `json:"disk_bytes"`
	LastDeployAt     string          `json:"last_deploy_at,omitempty"`
	LastDeployResult string          `json:"last_deploy_result"`
}

type ListResult struct {
	Apps []AppView `json:"apps"`
}

type LogsResult struct {
	App     string `json:"app"`
	Service string `json:"service"`
	Logs    string `json:"logs"`
}

type DeleteResult struct {
	App     string `json:"app"`
	Deleted bool   `json:"deleted"`
}
