package service

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/sebbi08/deployment-gateway/internal/names"
	"github.com/sebbi08/deployment-gateway/internal/render"
	"github.com/sebbi08/deployment-gateway/internal/stack"
	"github.com/sebbi08/deployment-gateway/internal/store"
	"github.com/sebbi08/deployment-gateway/internal/tmpl"
	"github.com/sebbi08/deployment-gateway/internal/verify"
)

type planned struct {
	app      string
	stack    *stack.Stack
	out      render.Output
	policy   verify.Policy
	defaults stack.Defaults
	memBytes int64
	cpus     float64
	claims   []store.PortClaim
	// imagePrefixes / runtimePrefixes are the reserved label namespaces
	// checked on image LABELs before up and on container labels after up.
	imagePrefixes   []string
	runtimePrefixes []string
}

// Label namespaces that only the gateway may set. Compose itself adds
// com.docker.compose.* to containers, so that prefix is checked on images
// only. Every stored template's namespaces are reserved as well.
var (
	// Namespaces of label-driven tools that commonly run with the Docker
	// socket and act on any container's labels (ofelia/chadburn job-run
	// labels even start new containers with arbitrary options).
	labelDrivenTools = []string{"ofelia.", "chadburn.", "com.centurylinklabs.watchtower.", "docker-volume-backup.", "swarm.cronjob."}

	fixedImagePrefixes   = append([]string{"traefik.", "deploy-gateway.", "com.docker.compose."}, labelDrivenTools...)
	fixedRuntimePrefixes = append([]string{"traefik.", "deploy-gateway."}, labelDrivenTools...)
)

// reservedPrefixes returns the fixed prefixes plus the namespaces of every
// stored template (granted or not).
func reservedPrefixes(fixed []string, all []store.StoredTemplate) []string {
	ts := make([]tmpl.Template, 0, len(all))
	for _, t := range all {
		ts = append(ts, t.Template)
	}
	return slices.Compact(slices.Sorted(slices.Values(slices.Concat(fixed, tmpl.LabelPrefixes(ts)))))
}

// plan runs validation, quota checks, rendering and verification. It has no
// side effects.
func (s *Service) plan(ctx context.Context, ws store.Workspace, app string, st *stack.Stack) (*planned, error) {
	if !names.ValidApp(app) {
		return nil, validationError("app", appRule)
	}
	granted, err := s.Store.GrantedTemplates(ctx, ws.ID)
	if err != nil {
		return nil, err
	}
	templates := map[string]tmpl.Template{}
	grants := stack.Grants{}
	for _, t := range granted {
		templates[t.Name] = t.Template
		grants[t.Name] = t.Template.Shareable()
	}
	q := ws.Quota
	lim := stack.Limits{
		MaxServices: q.MaxServicesPerApp, MaxMemoryBytes: q.MaxMemoryMB << 20, MaxCPUs: q.MaxCPUs, MaxPids: q.MaxPids,
		PortRangeStart: ws.PortRangeStart, PortRangeEnd: ws.PortRangeEnd,
	}
	if errs := stack.Validate(st, lim, grants); errs != nil {
		return nil, &Error{Code: CodeValidation, Message: "stack validation failed", Fields: errs}
	}
	defaults := stack.Defaults{MemoryBytes: q.DefaultMemoryMB << 20, CPUs: q.DefaultCPUs, Pids: q.DefaultPids}
	mem, cpus := stack.Totals(st, defaults)
	usage, err := s.Store.WorkspaceUsage(ctx, ws.ID, app)
	if err != nil {
		return nil, err
	}
	_, err = s.Store.GetApp(ctx, ws.ID, app)
	isNew := errors.Is(err, store.ErrNotFound)
	if err != nil && !isNew {
		return nil, err
	}
	var qerrs []string
	if isNew && usage.Apps+1 > q.MaxApps {
		qerrs = append(qerrs, fmt.Sprintf("workspace app limit of %d reached", q.MaxApps))
	}
	if usage.MemoryBytes+mem > q.MaxTotalMemoryMB<<20 {
		qerrs = append(qerrs, fmt.Sprintf("total memory would be %dm, limit is %dm", (usage.MemoryBytes+mem)>>20, q.MaxTotalMemoryMB))
	}
	if usage.CPUs+cpus > q.MaxTotalCPUs+1e-9 {
		qerrs = append(qerrs, fmt.Sprintf("total cpus would be %.2f, limit is %.2f", usage.CPUs+cpus, q.MaxTotalCPUs))
	}
	if len(qerrs) > 0 {
		return nil, &Error{Code: CodeQuota, Message: strings.Join(qerrs, "; ")}
	}
	dataDir := s.dataDir(ws.Name, app)
	out, err := render.Render(render.Input{
		Workspace: ws.Name, App: app, Stack: st, Templates: templates,
		Defaults: defaults, DataDir: dataDir, BindIP: ws.PortBindIP,
	})
	var fe *render.FieldError
	if errors.As(err, &fe) {
		return nil, &Error{Code: CodeValidation, Message: "stack validation failed", Fields: stack.Errors{{Field: fe.Field, Message: fe.Message}}}
	}
	if err != nil {
		return nil, err
	}
	nets := map[string]bool{names.Network(ws.Name, app): true}
	for _, t := range templates {
		for _, n := range t.Networks {
			nets[n] = true
		}
	}
	pol := verify.Policy{
		Workspace: ws.Name, App: app, DataDir: dataDir, Networks: nets,
		MaxMemoryBytes: lim.MaxMemoryBytes, MaxCPUs: q.MaxCPUs, MaxPids: q.MaxPids,
		PortRangeStart: ws.PortRangeStart, PortRangeEnd: ws.PortRangeEnd, BindIP: ws.PortBindIP,
	}
	if err := verify.Verify(out.YAML, pol); err != nil {
		return nil, &Error{Code: CodeSecurity, Message: "rendered configuration failed verification", detail: err.Error(), critical: true}
	}
	all, err := s.Store.ListTemplates(ctx)
	if err != nil {
		return nil, err
	}
	return &planned{app: app, stack: st, out: out, policy: pol, defaults: defaults, memBytes: mem, cpus: cpus,
		claims:          claimsFrom(app, out.Ports),
		imagePrefixes:   reservedPrefixes(slices.Concat(fixedImagePrefixes, s.ExtraReservedPrefixes), all),
		runtimePrefixes: reservedPrefixes(slices.Concat(fixedRuntimePrefixes, s.ExtraReservedPrefixes), all)}, nil
}

func claimsFrom(app string, ports []render.PublishedPort) []store.PortClaim {
	out := []store.PortClaim{}
	for _, p := range ports {
		out = append(out, store.PortClaim{Protocol: p.Protocol, HostPort: p.Host, ContainerPort: p.Container, Service: p.Service, App: app})
	}
	return out
}

func portsFor(claims []store.PortClaim, svc string) []store.PortClaim {
	out := []store.PortClaim{}
	for _, c := range claims {
		if c.Service == svc {
			out = append(out, c)
		}
	}
	return out
}

func hostURL(h string) string {
	if h == "" {
		return ""
	}
	return "https://" + h
}

func (pl *planned) servicePlans(ws string) []ServicePlan {
	out := []ServicePlan{}
	for _, svc := range stack.ServiceNames(pl.stack) {
		r := stack.Effective(pl.stack.Services[svc], pl.defaults)
		out = append(out, ServicePlan{
			Service: svc, InternalHost: names.Container(ws, pl.app, svc), URL: hostURL(pl.out.Hosts[svc]),
			Ports: portsFor(pl.claims, svc), MemoryMB: r.MemoryBytes >> 20, CPUs: r.CPUs,
		})
	}
	return out
}

func (s *Service) checkPortConflicts(ctx context.Context, ws store.Workspace, app string, claims []store.PortClaim) error {
	conflicts, err := s.Store.PortConflicts(ctx, ws.ID, app, claims)
	if err != nil {
		return err
	}
	if len(conflicts) > 0 {
		return &Error{Code: CodePortTaken, Message: (&store.PortTakenError{Claim: conflicts[0]}).Error()}
	}
	return nil
}
