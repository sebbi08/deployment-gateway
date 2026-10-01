package service

import (
	"context"
	"fmt"
	"time"
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
	ws := p.Workspace
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
	granted, err := s.Store.GrantedTemplates(ctx, p.Workspace.ID)
	if err != nil {
		return TemplatesResult{}, err
	}
	res.Templates = []TemplateView{}
	for _, t := range granted {
		res.Templates = append(res.Templates, TemplateView{Name: t.Name, Description: t.Description, HostPattern: t.HostPattern, Shareable: t.Template.Shareable()})
	}
	return res, nil
}
