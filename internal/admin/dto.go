package admin

import (
	"context"
	"encoding/json"
	"time"

	"github.com/sebbi08/deployment-gateway/internal/service"
	"github.com/sebbi08/deployment-gateway/internal/store"
)

// JSON shapes of the admin API; mirrored in web/src/types.ts.

type usageDTO struct {
	Apps     int     `json:"apps"`
	MemoryMB int64   `json:"memory_mb"`
	CPUs     float64 `json:"cpus"`
}

type workspaceRow struct {
	Name  string               `json:"name"`
	Quota store.Quota          `json:"quota"`
	Usage usageDTO             `json:"usage"`
	Ports service.PortSettings `json:"ports"`
}

type grantDTO struct {
	ID          int64  `json:"id"`
	Name        string `json:"name"`
	HostPattern string `json:"host_pattern"`
	Granted     bool   `json:"granted"`
}

type tokenDTO struct {
	ID         int64      `json:"id"`
	Label      string     `json:"label"`
	Prefix     string     `json:"prefix"`
	CreatedAt  time.Time  `json:"created_at"`
	LastUsedAt *time.Time `json:"last_used_at"`
	RevokedAt  *time.Time `json:"revoked_at"`
}

type workspaceDetail struct {
	workspaceRow
	Folder    string            `json:"folder"`
	Templates []grantDTO        `json:"templates"`
	Tokens    []tokenDTO        `json:"tokens"`
	Apps      []service.AppView `json:"apps"`
}

type auditDTO struct {
	ID         int64           `json:"id"`
	Time       time.Time       `json:"time"`
	ActorType  string          `json:"actor_type"`
	Actor      string          `json:"actor"`
	Workspace  string          `json:"workspace"`
	Action     string          `json:"action"`
	Params     json.RawMessage `json:"params"`
	Result     string          `json:"result"`
	Error      string          `json:"error"`
	Severity   string          `json:"severity"`
	DurationMS int64           `json:"duration_ms"`
	ClientIP   string          `json:"client_ip"`
}

func toAuditDTOs(entries []store.AuditEntry) []auditDTO {
	out := make([]auditDTO, 0, len(entries))
	for _, e := range entries {
		params := json.RawMessage(e.Params)
		if !json.Valid(params) {
			params, _ = json.Marshal(e.Params)
		}
		out = append(out, auditDTO{ID: e.ID, Time: e.Time, ActorType: e.ActorType, Actor: e.Actor, Workspace: e.Workspace,
			Action: e.Action, Params: params, Result: e.Result, Error: e.Error, Severity: e.Severity,
			DurationMS: e.DurationMS, ClientIP: e.ClientIP})
	}
	return out
}

func (s *Server) row(ctx context.Context, w store.Workspace) (workspaceRow, error) {
	u, err := s.opts.Store.WorkspaceUsage(ctx, w.ID, "")
	if err != nil {
		return workspaceRow{}, err
	}
	return workspaceRow{
		Name: w.Name, Quota: w.Quota,
		Usage: usageDTO{Apps: u.Apps, MemoryMB: u.MemoryBytes >> 20, CPUs: u.CPUs},
		Ports: service.PortSettings{Start: w.PortRangeStart, End: w.PortRangeEnd, BindIP: w.PortBindIP},
	}, nil
}

func (s *Server) rows(ctx context.Context) ([]workspaceRow, error) {
	list, err := s.opts.Store.ListWorkspaces(ctx)
	if err != nil {
		return nil, err
	}
	out := []workspaceRow{}
	for _, w := range list {
		r, err := s.row(ctx, w)
		if err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, nil
}
