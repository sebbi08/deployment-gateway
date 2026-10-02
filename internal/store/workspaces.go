package store

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"
)

type Quota struct {
	MaxApps           int     `json:"max_apps"`
	MaxServicesPerApp int     `json:"max_services_per_app"`
	MaxTotalMemoryMB  int64   `json:"max_total_memory_mb"`
	MaxTotalCPUs      float64 `json:"max_total_cpus"`
	DefaultMemoryMB   int64   `json:"default_memory_mb"`
	MaxMemoryMB       int64   `json:"max_memory_mb"`
	DefaultCPUs       float64 `json:"default_cpus"`
	MaxCPUs           float64 `json:"max_cpus"`
	DefaultPids       int64   `json:"default_pids"`
	MaxPids           int64   `json:"max_pids"`
}

// BuiltinDefaultQuota is deliberately small so a single workspace cannot
// exhaust a small, shared host.
func BuiltinDefaultQuota() Quota {
	return Quota{
		MaxApps: 5, MaxServicesPerApp: 4,
		MaxTotalMemoryMB: 2048, MaxTotalCPUs: 2,
		DefaultMemoryMB: 256, MaxMemoryMB: 1024,
		DefaultCPUs: 0.5, MaxCPUs: 1,
		DefaultPids: 256, MaxPids: 1024,
	}
}

func (q Quota) Validate() error {
	var errs []string
	check := func(ok bool, msg string) {
		if !ok {
			errs = append(errs, msg)
		}
	}
	check(q.MaxApps >= 1, "max_apps must be at least 1")
	check(q.MaxServicesPerApp >= 1 && q.MaxServicesPerApp <= 16, "max_services_per_app must be 1-16")
	check(q.MaxTotalMemoryMB >= 16, "max_total_memory_mb must be at least 16")
	check(q.MaxTotalCPUs > 0, "max_total_cpus must be positive")
	check(q.DefaultMemoryMB >= 16 && q.DefaultMemoryMB <= q.MaxMemoryMB, "default_memory_mb must be 16..max_memory_mb")
	check(q.MaxMemoryMB <= q.MaxTotalMemoryMB, "max_memory_mb must not exceed max_total_memory_mb")
	check(q.DefaultCPUs > 0 && q.DefaultCPUs <= q.MaxCPUs, "default_cpus must be >0 and <= max_cpus")
	check(q.MaxCPUs <= q.MaxTotalCPUs, "max_cpus must not exceed max_total_cpus")
	check(q.DefaultPids >= 16 && q.DefaultPids <= q.MaxPids, "default_pids must be 16..max_pids")
	if len(errs) > 0 {
		return errors.New(strings.Join(errs, "; "))
	}
	return nil
}

type Workspace struct {
	ID             int64     `json:"-"`
	Name           string    `json:"name"`
	CreatedAt      time.Time `json:"created_at"`
	Quota          Quota     `json:"quota"`
	PortRangeStart int       `json:"port_range_start"`
	PortRangeEnd   int       `json:"port_range_end"`
	PortBindIP     string    `json:"port_bind_ip"`
}

const wsCols = `id, name, created_at, max_apps, max_services_per_app, max_total_memory_mb, max_total_cpus,
	default_memory_mb, max_memory_mb, default_cpus, max_cpus, default_pids, max_pids,
	port_range_start, port_range_end, port_bind_ip`

func scanWorkspace(sc scanner) (Workspace, error) {
	var w Workspace
	var created int64
	q := &w.Quota
	err := sc.Scan(&w.ID, &w.Name, &created, &q.MaxApps, &q.MaxServicesPerApp, &q.MaxTotalMemoryMB, &q.MaxTotalCPUs,
		&q.DefaultMemoryMB, &q.MaxMemoryMB, &q.DefaultCPUs, &q.MaxCPUs, &q.DefaultPids, &q.MaxPids,
		&w.PortRangeStart, &w.PortRangeEnd, &w.PortBindIP)
	w.CreatedAt = time.Unix(created, 0)
	return w, notFound(err)
}

func (s *Store) CreateWorkspace(ctx context.Context, w Workspace) (Workspace, error) {
	q := w.Quota
	w.CreatedAt = time.Unix(s.now().Unix(), 0)
	res, err := s.db.ExecContext(ctx, `INSERT INTO workspaces (name, created_at, max_apps, max_services_per_app,
		max_total_memory_mb, max_total_cpus, default_memory_mb, max_memory_mb, default_cpus, max_cpus,
		default_pids, max_pids, port_range_start, port_range_end, port_bind_ip)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		w.Name, w.CreatedAt.Unix(), q.MaxApps, q.MaxServicesPerApp, q.MaxTotalMemoryMB, q.MaxTotalCPUs,
		q.DefaultMemoryMB, q.MaxMemoryMB, q.DefaultCPUs, q.MaxCPUs, q.DefaultPids, q.MaxPids,
		w.PortRangeStart, w.PortRangeEnd, w.PortBindIP)
	if isUnique(err) {
		return Workspace{}, ErrExists
	}
	if err != nil {
		return Workspace{}, err
	}
	w.ID, err = res.LastInsertId()
	return w, err
}

func (s *Store) UpdateWorkspace(ctx context.Context, w Workspace) error {
	q := w.Quota
	res, err := s.db.ExecContext(ctx, `UPDATE workspaces SET max_apps=?, max_services_per_app=?, max_total_memory_mb=?,
		max_total_cpus=?, default_memory_mb=?, max_memory_mb=?, default_cpus=?, max_cpus=?, default_pids=?, max_pids=?,
		port_range_start=?, port_range_end=?, port_bind_ip=? WHERE id=?`,
		q.MaxApps, q.MaxServicesPerApp, q.MaxTotalMemoryMB, q.MaxTotalCPUs, q.DefaultMemoryMB, q.MaxMemoryMB,
		q.DefaultCPUs, q.MaxCPUs, q.DefaultPids, q.MaxPids, w.PortRangeStart, w.PortRangeEnd, w.PortBindIP, w.ID)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

func (s *Store) GetWorkspace(ctx context.Context, id int64) (Workspace, error) {
	return scanWorkspace(s.db.QueryRowContext(ctx, `SELECT `+wsCols+` FROM workspaces WHERE id=?`, id))
}

func (s *Store) GetWorkspaceByName(ctx context.Context, name string) (Workspace, error) {
	return scanWorkspace(s.db.QueryRowContext(ctx, `SELECT `+wsCols+` FROM workspaces WHERE name=?`, name))
}

func (s *Store) ListWorkspaces(ctx context.Context) ([]Workspace, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT `+wsCols+` FROM workspaces ORDER BY name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Workspace{}
	for rows.Next() {
		w, err := scanWorkspace(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, w)
	}
	return out, rows.Err()
}

func (s *Store) DeleteWorkspace(ctx context.Context, id int64) error {
	res, err := s.db.ExecContext(ctx, `DELETE FROM workspaces WHERE id=?`, id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// OverlappingWorkspace returns the name of a workspace (other than
// excludeID) whose port range intersects [start, end], or "".
func (s *Store) OverlappingWorkspace(ctx context.Context, start, end int, excludeID int64) (string, error) {
	var name string
	err := s.db.QueryRowContext(ctx, `SELECT name FROM workspaces
		WHERE id != ? AND port_range_start > 0 AND port_range_start <= ? AND port_range_end >= ?
		ORDER BY name LIMIT 1`, excludeID, end, start).Scan(&name)
	if err != nil && !errors.Is(notFound(err), ErrNotFound) {
		return "", fmt.Errorf("store: overlap query: %w", err)
	}
	return name, nil
}
