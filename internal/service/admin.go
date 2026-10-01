package service

import (
	"context"
	"errors"
	"fmt"
	"net/netip"
	"os"
	"time"

	"github.com/sebmahr/deploy-gateway/internal/names"
	"github.com/sebmahr/deploy-gateway/internal/store"
	"github.com/sebmahr/deploy-gateway/internal/tmpl"
)

type PortSettings struct {
	Start  int    `json:"start"`
	End    int    `json:"end"`
	BindIP string `json:"bind_ip"`
}

func (s *Service) validatePortSettings(ctx context.Context, ps PortSettings, excludeID int64) error {
	if !(ps.Start == 0 && ps.End == 0) && (ps.Start < 1024 || ps.End > 65535 || ps.Start > ps.End) {
		return validationError("ports", "port range must be within 1024-65535 with start <= end (or 0-0 to disable publishing)")
	}
	if _, err := netip.ParseAddr(ps.BindIP); err != nil {
		return validationError("bind_ip", "bind IP must be an IP address")
	}
	if ps.Start > 0 {
		name, err := s.Store.OverlappingWorkspace(ctx, ps.Start, ps.End, excludeID)
		if err != nil {
			return err
		}
		if name != "" {
			return validationError("ports", fmt.Sprintf("port range overlaps workspace %q", name))
		}
	}
	return nil
}

func (s *Service) CreateWorkspace(ctx context.Context, actor, name string, q store.Quota, ps PortSettings) (w store.Workspace, err error) {
	defer s.adminTrack(ctx, actor, name, "create_workspace", map[string]any{"quota": q, "ports": ps}, &err, time.Now())
	if !names.ValidWorkspace(name) {
		return w, validationError("name", "workspace name must match ^[a-z][a-z0-9]{1,15}$")
	}
	if err = q.Validate(); err != nil {
		return w, validationError("quota", err.Error())
	}
	if err = s.validatePortSettings(ctx, ps, 0); err != nil {
		return w, err
	}
	if err = os.MkdirAll(s.workspaceDir(name), 0o750); err != nil {
		return w, err
	}
	w, err = s.Store.CreateWorkspace(ctx, store.Workspace{Name: name, Quota: q, PortRangeStart: ps.Start, PortRangeEnd: ps.End, PortBindIP: ps.BindIP})
	if errors.Is(err, store.ErrExists) {
		return w, validationError("name", "workspace already exists")
	}
	return w, err
}

// SaveTemplate creates (id == 0) or updates a template after structural
// validation, hostname-uniqueness validation and a Docker network check.
func (s *Service) SaveTemplate(ctx context.Context, actor string, id int64, t tmpl.Template) (newID int64, err error) {
	defer s.adminTrack(ctx, actor, "", "save_template", t, &err, time.Now())
	all, err := s.Store.ListTemplates(ctx)
	if err != nil {
		return 0, err
	}
	var others []tmpl.Template
	for _, o := range all {
		if o.ID != id {
			others = append(others, o.Template)
		}
	}
	if err = tmpl.Validate(t, others); err != nil {
		return 0, &Error{Code: CodeValidation, Message: err.Error()}
	}
	for _, n := range t.Networks {
		ok, err := s.Docker.NetworkExists(ctx, n)
		if err != nil {
			return 0, err
		}
		if !ok {
			return 0, validationError("networks", fmt.Sprintf("network %q does not exist in Docker", n))
		}
	}
	if id == 0 {
		newID, err = s.Store.CreateTemplate(ctx, t)
	} else {
		newID, err = id, s.Store.UpdateTemplate(ctx, id, t)
	}
	if errors.Is(err, store.ErrExists) {
		return 0, validationError("name", "a template with this name already exists")
	}
	return newID, err
}

func (s *Service) SetGrants(ctx context.Context, actor string, w store.Workspace, templateIDs []int64) (err error) {
	defer s.adminTrack(ctx, actor, w.Name, "set_grants", templateIDs, &err, time.Now())
	return s.Store.SetGrants(ctx, w.ID, templateIDs)
}
