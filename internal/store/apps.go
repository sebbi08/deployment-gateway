package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"
)

type App struct {
	ID               int64
	WorkspaceID      int64
	Name             string
	MemoryBytes      int64
	CPUs             float64
	Hosts            map[string]string
	CreatedAt        time.Time
	UpdatedAt        time.Time
	LastDeployAt     *time.Time
	LastDeployResult string
}

type PortClaim struct {
	Protocol      string `json:"protocol"`
	HostPort      int    `json:"host_port"`
	ContainerPort int    `json:"container_port"`
	Service       string `json:"service"`
	App           string `json:"app"`
}

type PortTakenError struct{ Claim PortClaim }

func (e *PortTakenError) Error() string {
	return fmt.Sprintf("host port %s/%d is already used by app %q", e.Claim.Protocol, e.Claim.HostPort, e.Claim.App)
}

type Usage struct {
	Apps        int
	MemoryBytes int64
	CPUs        float64
}

const appCols = `id, workspace_id, name, memory_bytes, cpus, hosts_json, created_at, updated_at, last_deploy_at, last_deploy_result`

func scanApp(sc scanner) (App, error) {
	var a App
	var hosts string
	var created, updated int64
	var deployed sql.NullInt64
	if err := sc.Scan(&a.ID, &a.WorkspaceID, &a.Name, &a.MemoryBytes, &a.CPUs, &hosts, &created, &updated, &deployed, &a.LastDeployResult); err != nil {
		return App{}, notFound(err)
	}
	a.CreatedAt, a.UpdatedAt, a.LastDeployAt = time.Unix(created, 0), time.Unix(updated, 0), timePtr(deployed)
	if err := json.Unmarshal([]byte(hosts), &a.Hosts); err != nil {
		return App{}, err
	}
	return a, nil
}

func (s *Store) GetApp(ctx context.Context, wsID int64, name string) (App, error) {
	return scanApp(s.db.QueryRowContext(ctx, `SELECT `+appCols+` FROM apps WHERE workspace_id=? AND name=?`, wsID, name))
}

func (s *Store) ListApps(ctx context.Context, wsID int64) ([]App, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT `+appCols+` FROM apps WHERE workspace_id=? ORDER BY name`, wsID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []App{}
	for rows.Next() {
		a, err := scanApp(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

// AppKeys returns "<workspace>/<app>" for every app (used to find orphans).
func (s *Store) AppKeys(ctx context.Context) (map[string]bool, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT w.name, a.name FROM apps a JOIN workspaces w ON w.id = a.workspace_id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]bool{}
	for rows.Next() {
		var ws, app string
		if err := rows.Scan(&ws, &app); err != nil {
			return nil, err
		}
		out[ws+"/"+app] = true
	}
	return out, rows.Err()
}

// SaveDeployment upserts the app record and replaces its port claims in one
// transaction. On a claim conflict nothing is changed.
func (s *Store) SaveDeployment(ctx context.Context, wsID int64, name string, mem int64, cpus float64, hosts map[string]string, claims []PortClaim) (App, error) {
	if hosts == nil {
		hosts = map[string]string{}
	}
	hostsJSON, _ := json.Marshal(hosts)
	now := s.now().Unix()
	err := s.withTx(ctx, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, `INSERT INTO apps (workspace_id, name, memory_bytes, cpus, hosts_json, created_at, updated_at)
			VALUES (?, ?, ?, ?, ?, ?, ?)
			ON CONFLICT (workspace_id, name) DO UPDATE SET memory_bytes=excluded.memory_bytes, cpus=excluded.cpus,
			hosts_json=excluded.hosts_json, updated_at=excluded.updated_at`,
			wsID, name, mem, cpus, string(hostsJSON), now, now); err != nil {
			return err
		}
		var appID int64
		if err := tx.QueryRowContext(ctx, `SELECT id FROM apps WHERE workspace_id=? AND name=?`, wsID, name).Scan(&appID); err != nil {
			return err
		}
		for _, c := range claims {
			var other PortClaim
			err := tx.QueryRowContext(ctx, `SELECT pc.protocol, pc.host_port, pc.container_port, pc.service, a.name
				FROM port_claims pc JOIN apps a ON a.id = pc.app_id
				WHERE pc.protocol=? AND pc.host_port=? AND pc.app_id != ?`, c.Protocol, c.HostPort, appID).
				Scan(&other.Protocol, &other.HostPort, &other.ContainerPort, &other.Service, &other.App)
			if err == nil {
				return &PortTakenError{Claim: other}
			}
			if !errors.Is(err, sql.ErrNoRows) {
				return err
			}
		}
		if _, err := tx.ExecContext(ctx, `DELETE FROM port_claims WHERE app_id=?`, appID); err != nil {
			return err
		}
		for _, c := range claims {
			if _, err := tx.ExecContext(ctx, `INSERT INTO port_claims (protocol, host_port, container_port, service, app_id) VALUES (?, ?, ?, ?, ?)`,
				c.Protocol, c.HostPort, c.ContainerPort, c.Service, appID); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return App{}, err
	}
	return s.GetApp(ctx, wsID, name)
}

// PortConflicts returns existing claims on the given ports held by any app
// other than (wsID, app).
func (s *Store) PortConflicts(ctx context.Context, wsID int64, app string, claims []PortClaim) ([]PortClaim, error) {
	out := []PortClaim{}
	for _, c := range claims {
		var other PortClaim
		err := s.db.QueryRowContext(ctx, `SELECT pc.protocol, pc.host_port, pc.container_port, pc.service, a.name
			FROM port_claims pc JOIN apps a ON a.id = pc.app_id
			WHERE pc.protocol=? AND pc.host_port=? AND NOT (a.workspace_id=? AND a.name=?)`, c.Protocol, c.HostPort, wsID, app).
			Scan(&other.Protocol, &other.HostPort, &other.ContainerPort, &other.Service, &other.App)
		if errors.Is(err, sql.ErrNoRows) {
			continue
		}
		if err != nil {
			return nil, err
		}
		out = append(out, other)
	}
	return out, nil
}

func (s *Store) ListPortClaims(ctx context.Context, wsID int64) ([]PortClaim, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT pc.protocol, pc.host_port, pc.container_port, pc.service, a.name
		FROM port_claims pc JOIN apps a ON a.id = pc.app_id WHERE a.workspace_id=? ORDER BY pc.host_port, pc.protocol`, wsID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []PortClaim{}
	for rows.Next() {
		var c PortClaim
		if err := rows.Scan(&c.Protocol, &c.HostPort, &c.ContainerPort, &c.Service, &c.App); err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

func (s *Store) RecordDeploy(ctx context.Context, appID int64, result string) error {
	_, err := s.db.ExecContext(ctx, `UPDATE apps SET last_deploy_at=?, last_deploy_result=? WHERE id=?`, s.now().Unix(), result, appID)
	return err
}

// RaiseReservation sets the app's memory and CPU reservation to at least the
// given values (used while containers of a previous stack may still run).
func (s *Store) RaiseReservation(ctx context.Context, appID int64, mem int64, cpus float64) error {
	_, err := s.db.ExecContext(ctx, `UPDATE apps SET memory_bytes=MAX(memory_bytes, ?), cpus=MAX(cpus, ?) WHERE id=?`, mem, cpus, appID)
	return err
}

func (s *Store) DeleteApp(ctx context.Context, appID int64) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM apps WHERE id=?`, appID)
	return err
}

// WorkspaceUsage sums all apps of a workspace except excludeApp (running or
// stopped alike).
func (s *Store) WorkspaceUsage(ctx context.Context, wsID int64, excludeApp string) (Usage, error) {
	var u Usage
	err := s.db.QueryRowContext(ctx, `SELECT COUNT(*), COALESCE(SUM(memory_bytes), 0), COALESCE(SUM(cpus), 0)
		FROM apps WHERE workspace_id=? AND name != ?`, wsID, excludeApp).Scan(&u.Apps, &u.MemoryBytes, &u.CPUs)
	return u, err
}

// AppHosts returns the public hosts of every app, keyed "<workspace>/<app>".
func (s *Store) AppHosts(ctx context.Context) (map[string][]string, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT w.name, a.name, a.hosts_json FROM apps a JOIN workspaces w ON w.id = a.workspace_id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string][]string{}
	for rows.Next() {
		var ws, app, hostsJSON string
		if err := rows.Scan(&ws, &app, &hostsJSON); err != nil {
			return nil, err
		}
		var hosts map[string]string
		if err := json.Unmarshal([]byte(hostsJSON), &hosts); err != nil {
			return nil, err
		}
		for _, h := range hosts {
			out[ws+"/"+app] = append(out[ws+"/"+app], h)
		}
	}
	return out, rows.Err()
}

// AppRef is an app with the name of its workspace.
type AppRef struct {
	Workspace string
	App       App
}

// AppsWithResult returns every app whose last deploy result is result.
func (s *Store) AppsWithResult(ctx context.Context, result string) ([]AppRef, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT w.name, a.id FROM apps a JOIN workspaces w ON w.id = a.workspace_id WHERE a.last_deploy_result=?`, result)
	if err != nil {
		return nil, err
	}
	type ref struct {
		ws string
		id int64
	}
	var refs []ref
	for rows.Next() {
		var r ref
		if err := rows.Scan(&r.ws, &r.id); err != nil {
			rows.Close()
			return nil, err
		}
		refs = append(refs, r)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	out := make([]AppRef, 0, len(refs))
	for _, r := range refs {
		a, err := scanApp(s.db.QueryRowContext(ctx, `SELECT `+appCols+` FROM apps WHERE id=?`, r.id))
		if err != nil {
			return nil, err
		}
		out = append(out, AppRef{Workspace: r.ws, App: a})
	}
	return out, nil
}
