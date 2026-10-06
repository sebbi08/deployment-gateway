package service

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/sebbi08/deployment-gateway/internal/dockerx"
	"github.com/sebbi08/deployment-gateway/internal/names"
	"github.com/sebbi08/deployment-gateway/internal/store"
)

const namingDoc = "Containers, compose services and DNS names are <workspace>-<app>-<service>. " +
	"Services of one app reach each other only by that full name on the app's private network " +
	"(e.g. postgres://alice-shop-db:5432). There are no short aliases."

var guidance = []string{
	"Images must be pinned (explicit tag or digest); :latest is rejected. Build and push images in your own CI; the server pulls them on every deploy.",
	`Containers run with all capabilities dropped and no-new-privileges. Images whose entrypoint starts as root and switches user (official postgres, redis, ...) need "user": "<uid>:<gid>" (e.g. "999:999"); the volume directory is then owned by that uid.`,
	"The root filesystem is read-only and /tmp is a tmpfs. Set writable_rootfs: true if the image must write elsewhere, or use a volume.",
	"Use validate before deploy to see hostnames, URLs, published ports and resource totals without starting anything.",
	"Environment values are passed literally; $ is never interpolated.",
}

func (s *Service) Whoami(ctx context.Context, p Principal) (res WhoamiResult, err error) {
	defer s.track(ctx, p, "whoami", nil, &err, time.Now())
	ws, err := s.current(ctx, p)
	if err != nil {
		return WhoamiResult{}, err
	}
	u, err := s.Store.WorkspaceUsage(ctx, ws.ID, "")
	if err != nil {
		return WhoamiResult{}, err
	}
	claims, err := s.Store.ListPortClaims(ctx, ws.ID)
	if err != nil {
		return WhoamiResult{}, err
	}
	pr := ""
	if ws.PortRangeStart > 0 {
		pr = fmt.Sprintf("%d-%d", ws.PortRangeStart, ws.PortRangeEnd)
	}
	return WhoamiResult{
		Workspace: ws.Name, Quota: ws.Quota,
		Usage:     Usage{Apps: u.Apps, MemoryMB: u.MemoryBytes >> 20, CPUs: u.CPUs},
		PortRange: pr, PortBindIP: ws.PortBindIP, PortsInUse: claims,
		Naming: namingDoc, Guidance: guidance,
	}, nil
}

func (s *Service) ListTemplates(ctx context.Context, p Principal) (res TemplatesResult, err error) {
	defer s.track(ctx, p, "list_templates", nil, &err, time.Now())
	ws, err := s.current(ctx, p)
	if err != nil {
		return TemplatesResult{}, err
	}
	granted, err := s.Store.GrantedTemplates(ctx, ws.ID)
	if err != nil {
		return TemplatesResult{}, err
	}
	res.Templates = []TemplateView{}
	for _, t := range granted {
		res.Templates = append(res.Templates, TemplateView{Name: t.Name, Description: t.Description, HostPattern: t.HostPattern, Shareable: t.Template.Shareable()})
	}
	return res, nil
}

func (s *Service) List(ctx context.Context, p Principal) (res ListResult, err error) {
	defer s.track(ctx, p, "list", nil, &err, time.Now())
	ws, err := s.current(ctx, p)
	if err != nil {
		return ListResult{}, err
	}
	apps, err := s.appViews(ctx, ws)
	if err != nil {
		return ListResult{}, err
	}
	return ListResult{Apps: apps}, nil
}

// appViews builds app status from DB records (hosts, claims) plus live
// container state; it never consults templates.
func (s *Service) appViews(ctx context.Context, ws store.Workspace) ([]AppView, error) {
	apps, err := s.Store.ListApps(ctx, ws.ID)
	if err != nil {
		return nil, err
	}
	claims, err := s.Store.ListPortClaims(ctx, ws.ID)
	if err != nil {
		return nil, err
	}
	out := []AppView{}
	for _, a := range apps {
		var appClaims []store.PortClaim
		for _, c := range claims {
			if c.App == a.Name {
				appClaims = append(appClaims, c)
			}
		}
		st, err := s.status(ctx, ws, a.Name, a.Hosts, appClaims, nil)
		if err != nil {
			return nil, err
		}
		v := AppView{App: a.Name, Services: st.Services, DiskBytes: s.disk.usage(s.appDir(ws.Name, a.Name)), LastDeployResult: a.LastDeployResult}
		if a.LastDeployAt != nil {
			v.LastDeployAt = a.LastDeployAt.UTC().Format(time.RFC3339)
		}
		out = append(out, v)
	}
	return out, nil
}

func (s *Service) Logs(ctx context.Context, p Principal, in LogsInput) (res LogsResult, err error) {
	defer s.track(ctx, p, "logs", in, &err, time.Now())
	ws, err := s.current(ctx, p)
	if err != nil {
		return LogsResult{}, err
	}
	if _, err = s.ownedApp(ctx, ws, in.App); err != nil {
		return LogsResult{}, err
	}
	if !names.ValidService(in.Service) {
		return LogsResult{}, notFoundError("service")
	}
	tail := in.Tail
	if tail <= 0 {
		tail = 200
	}
	tail = min(tail, 1000)
	if in.Since != "" && !validSince(in.Since) {
		return LogsResult{}, validationError("since", "since must be a duration like 10m or an RFC3339 timestamp")
	}
	c, err := s.findContainer(ctx, ws.Name, in.App, in.Service)
	if err != nil {
		return LogsResult{}, err
	}
	logs, err := s.Docker.Logs(ctx, c.ID, tail, in.Since, 256<<10)
	if err != nil {
		return LogsResult{}, err
	}
	return LogsResult{App: in.App, Service: in.Service, Logs: logs}, nil
}

func validSince(v string) bool {
	if _, err := time.ParseDuration(v); err == nil {
		return true
	}
	_, err := time.Parse(time.RFC3339, v)
	return err == nil
}

// ownedApp resolves an app strictly within the caller's workspace.
func (s *Service) ownedApp(ctx context.Context, ws store.Workspace, app string) (store.App, error) {
	if !names.ValidApp(app) {
		return store.App{}, notFoundError("app")
	}
	a, err := s.Store.GetApp(ctx, ws.ID, app)
	if errors.Is(err, store.ErrNotFound) {
		return store.App{}, notFoundError("app")
	}
	return a, err
}

func (s *Service) findContainer(ctx context.Context, ws, app, svc string) (dockerx.ContainerInfo, error) {
	containers, err := s.Docker.ProjectContainers(ctx, ws, app)
	if err != nil {
		return dockerx.ContainerInfo{}, err
	}
	for _, c := range containers {
		if c.Labels[names.LabelWorkspace] == ws && c.Labels[names.LabelApp] == app && c.Labels[names.LabelService] == svc {
			return c, nil
		}
	}
	return dockerx.ContainerInfo{}, notFoundError("service")
}
