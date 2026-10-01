// Package render turns a validated stack into a hardened compose file
// (spec §9). Hardening is hard-coded here; neither templates nor agents can
// change it.
package render

import (
	"fmt"
	"path/filepath"
	"slices"
	"strconv"
	"strings"

	"go.yaml.in/yaml/v3"

	"github.com/sebmahr/deploy-gateway/internal/names"
	"github.com/sebmahr/deploy-gateway/internal/stack"
	"github.com/sebmahr/deploy-gateway/internal/tmpl"
)

type Input struct {
	Workspace string
	App       string
	Stack     *stack.Stack
	Templates map[string]tmpl.Template
	Defaults  stack.Defaults
	DataDir   string // <root>/<ws>/<app>/data
	BindIP    string
}

type PublishedPort struct {
	Service   string `json:"service"`
	Container int    `json:"container"`
	Host      int    `json:"host"`
	Protocol  string `json:"protocol"`
	HostIP    string `json:"host_ip"`
}

type Output struct {
	YAML  []byte
	Hosts map[string]string // short service name -> public host
	Ports []PublishedPort
}

func Render(in Input) (Output, error) {
	ws, app := in.Workspace, in.App
	privateNet := names.Network(ws, app)
	file := composeFile{
		Name:     names.Project(ws, app),
		Services: map[string]composeService{},
		Networks: map[string]composeNetwork{privateNet: {Name: privateNet}},
	}
	out := Output{Hosts: map[string]string{}, Ports: []PublishedPort{}}

	for _, svcName := range stack.ServiceNames(in.Stack) {
		svc := in.Stack.Services[svcName]
		id := names.Container(ws, app, svcName)
		res := stack.Effective(svc, in.Defaults)
		cs := composeService{
			Image:         svc.Image,
			ContainerName: id,
			Hostname:      id,
			Command:       escapeAll(svc.Command),
			Entrypoint:    escapeAll(svc.Entrypoint),
			Environment:   escapeMap(svc.Environment),
			User:          svc.User,
			Restart:       "unless-stopped",
			CapDrop:       []string{"ALL"},
			SecurityOpt:   []string{"no-new-privileges:true"},
			ReadOnly:      !svc.WritableRootfs,
			Tmpfs:         []string{"/tmp"},
			MemLimit:      res.MemoryBytes,
			MemswapLimit:  res.MemoryBytes,
			CPUs:          res.CPUs,
			PidsLimit:     res.Pids,
			Logging:       composeLogging{Driver: "json-file", Options: map[string]string{"max-size": "10m", "max-file": "3"}},
			Networks:      []string{privateNet},
			Labels:        map[string]string{},
		}
		if svc.Volume != nil {
			cs.Volumes = []composeVolume{{
				Type:   "bind",
				Source: filepath.Join(in.DataDir, svcName),
				Target: svc.Volume.Path,
				Bind:   composeBind{CreateHostPath: false},
			}}
		}
		for _, p := range svc.Ports {
			proto := stack.Protocol(p)
			cs.Ports = append(cs.Ports, composePort{Target: p.Container, Published: strconv.Itoa(p.Host), HostIP: in.BindIP, Protocol: proto})
			out.Ports = append(out.Ports, PublishedPort{Service: svcName, Container: p.Container, Host: p.Host, Protocol: proto, HostIP: in.BindIP})
		}
		if hc := svc.Healthcheck; hc != nil {
			interval, retries := hc.Interval, hc.Retries
			if interval == "" {
				interval = "30s"
			}
			if retries == 0 {
				retries = 3
			}
			cs.Healthcheck = &composeHealth{Test: append([]string{"CMD"}, escapeAll(hc.Command)...), Interval: interval, Retries: retries}
		}
		if len(svc.DependsOn) > 0 {
			cs.DependsOn = map[string]composeDepends{}
			for _, d := range svc.DependsOn {
				cond := "service_started"
				if in.Stack.Services[d].Healthcheck != nil {
					cond = "service_healthy"
				}
				cs.DependsOn[names.Container(ws, app, d)] = composeDepends{Condition: cond}
			}
		}
		if e := svc.Expose; e != nil {
			t, ok := in.Templates[e.Template]
			if !ok {
				return Output{}, fmt.Errorf("render: template %q not provided", e.Template)
			}
			vars := tmpl.Vars{Workspace: ws, App: app, Service: svcName, Port: e.Port}
			for k, v := range t.RenderLabels(vars) {
				cs.Labels[k] = escape(v)
			}
			for _, n := range t.Networks {
				if !slices.Contains(cs.Networks, n) {
					cs.Networks = append(cs.Networks, n)
				}
				file.Networks[n] = composeNetwork{Name: n, External: true}
			}
			if h := t.Host(vars); h != "" {
				out.Hosts[svcName] = h
			}
		}
		// Owner labels last so nothing can overwrite them.
		cs.Labels[names.LabelWorkspace] = ws
		cs.Labels[names.LabelApp] = app
		cs.Labels[names.LabelService] = svcName
		cs.Labels[names.LabelManaged] = "true"
		file.Services[id] = cs
	}

	y, err := yaml.Marshal(file)
	if err != nil {
		return Output{}, fmt.Errorf("render: marshal: %w", err)
	}
	out.YAML = y
	return out, nil
}

// escape prevents compose variable interpolation.
func escape(s string) string { return strings.ReplaceAll(s, "$", "$$") }

func escapeAll(in []string) []string {
	if in == nil {
		return nil
	}
	out := make([]string, len(in))
	for i, s := range in {
		out[i] = escape(s)
	}
	return out
}

func escapeMap(in map[string]string) map[string]string {
	if in == nil {
		return nil
	}
	out := make(map[string]string, len(in))
	for k, v := range in {
		out[k] = escape(v)
	}
	return out
}
