package store

import (
	"context"
	"database/sql"
	"time"
)

// MaxSecretsPerWorkspace caps set and requested secrets together.
const MaxSecretsPerWorkspace = 100

// Secret describes a workspace secret. The value is never part of it.
type Secret struct {
	ID, WorkspaceID      int64
	Name                 string
	Set                  bool
	CreatedAt, UpdatedAt time.Time
	RequestedAt          *time.Time
	RequestedBy          string
}

// SecretRef is one env var of one service bound to a secret.
type SecretRef struct {
	AppID                        int64
	App, Service, EnvKey, Secret string
	Stale                        bool
}

type SecretRefInput struct{ Service, EnvKey, Secret string }

func (s *Store) ListSecrets(ctx context.Context, wsID int64) ([]Secret, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id, workspace_id, name, value IS NOT NULL, created_at, updated_at, requested_at, requested_by
		FROM workspace_secrets WHERE workspace_id=? ORDER BY name`, wsID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Secret{}
	for rows.Next() {
		var x Secret
		var created, updated int64
		var requested sql.NullInt64
		if err := rows.Scan(&x.ID, &x.WorkspaceID, &x.Name, &x.Set, &created, &updated, &requested, &x.RequestedBy); err != nil {
			return nil, err
		}
		x.CreatedAt, x.UpdatedAt, x.RequestedAt = time.Unix(created, 0), time.Unix(updated, 0), timePtr(requested)
		out = append(out, x)
	}
	return out, rows.Err()
}

// SecretValues returns name -> value for the secrets of the workspace that have a value.
func (s *Store) SecretValues(ctx context.Context, wsID int64) (map[string]string, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT name, value FROM workspace_secrets WHERE workspace_id=? AND value IS NOT NULL`, wsID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]string{}
	for rows.Next() {
		var name, value string
		if err := rows.Scan(&name, &value); err != nil {
			return nil, err
		}
		out[name] = value
	}
	return out, rows.Err()
}

// RequestSecrets inserts the missing names as requested (no value). It stops
// silently once the workspace holds MaxSecretsPerWorkspace secrets.
func (s *Store) RequestSecrets(ctx context.Context, wsID int64, names []string, app string) error {
	now := s.now().Unix()
	return s.withTx(ctx, func(tx *sql.Tx) error {
		var count int
		if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM workspace_secrets WHERE workspace_id=?`, wsID).Scan(&count); err != nil {
			return err
		}
		for _, n := range names {
			if count >= MaxSecretsPerWorkspace {
				return nil
			}
			res, err := tx.ExecContext(ctx, `INSERT OR IGNORE INTO workspace_secrets (workspace_id, name, value, created_at, updated_at, requested_at, requested_by)
				VALUES (?, ?, NULL, ?, ?, ?, ?)`, wsID, n, now, now, now, app)
			if err != nil {
				return err
			}
			k, err := res.RowsAffected()
			if err != nil {
				return err
			}
			if k > 0 {
				count++
			}
		}
		return nil
	})
}

func (s *Store) CreateSecret(ctx context.Context, wsID int64, name, value string) error {
	now := s.now().Unix()
	_, err := s.db.ExecContext(ctx, `INSERT INTO workspace_secrets (workspace_id, name, value, created_at, updated_at) VALUES (?, ?, ?, ?, ?)`,
		wsID, name, value, now, now)
	if isUnique(err) {
		return ErrExists
	}
	return err
}

// SetSecretValue sets or replaces a value, and marks every ref to the secret
// stale. It returns those refs.
func (s *Store) SetSecretValue(ctx context.Context, wsID int64, name, value string) ([]SecretRef, error) {
	var refs []SecretRef
	err := s.withTx(ctx, func(tx *sql.Tx) error {
		var id int64
		if err := tx.QueryRowContext(ctx, `SELECT id FROM workspace_secrets WHERE workspace_id=? AND name=?`, wsID, name).Scan(&id); err != nil {
			return notFound(err)
		}
		if _, err := tx.ExecContext(ctx, `UPDATE workspace_secrets SET value=?, updated_at=?, requested_at=NULL WHERE id=?`, value, s.now().Unix(), id); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `UPDATE app_secret_refs SET stale=1 WHERE secret_id=?`, id); err != nil {
			return err
		}
		rows, err := tx.QueryContext(ctx, refSelect+` WHERE r.secret_id=? ORDER BY a.name, r.service, r.env_key`, id)
		if err != nil {
			return err
		}
		defer rows.Close()
		refs, err = scanRefs(rows)
		return err
	})
	if err != nil {
		return nil, err
	}
	return refs, nil
}

func (s *Store) DeleteSecret(ctx context.Context, wsID int64, name string) error {
	return s.withTx(ctx, func(tx *sql.Tx) error {
		var id int64
		if err := tx.QueryRowContext(ctx, `SELECT id FROM workspace_secrets WHERE workspace_id=? AND name=?`, wsID, name).Scan(&id); err != nil {
			return notFound(err)
		}
		var n int
		if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM app_secret_refs WHERE secret_id=?`, id).Scan(&n); err != nil {
			return err
		}
		if n > 0 {
			return ErrInUse
		}
		_, err := tx.ExecContext(ctx, `DELETE FROM workspace_secrets WHERE id=?`, id)
		return err
	})
}

const refSelect = `SELECT r.app_id, a.name, r.service, r.env_key, ws.name, r.stale
	FROM app_secret_refs r JOIN apps a ON a.id = r.app_id JOIN workspace_secrets ws ON ws.id = r.secret_id`

func scanRefs(rows *sql.Rows) ([]SecretRef, error) {
	out := []SecretRef{}
	for rows.Next() {
		var r SecretRef
		if err := rows.Scan(&r.AppID, &r.App, &r.Service, &r.EnvKey, &r.Secret, &r.Stale); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

func (s *Store) ListSecretRefs(ctx context.Context, wsID int64) ([]SecretRef, error) {
	rows, err := s.db.QueryContext(ctx, refSelect+` WHERE a.workspace_id=? ORDER BY a.name, r.service, r.env_key`, wsID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanRefs(rows)
}

func (s *Store) AppSecretRefs(ctx context.Context, appID int64) ([]SecretRef, error) {
	rows, err := s.db.QueryContext(ctx, refSelect+` WHERE r.app_id=? ORDER BY r.service, r.env_key`, appID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanRefs(rows)
}

// ClearStale clears the stale flag of the app's refs in the given services.
func (s *Store) ClearStale(ctx context.Context, appID int64, services []string) error {
	return s.withTx(ctx, func(tx *sql.Tx) error {
		for _, svc := range services {
			if _, err := tx.ExecContext(ctx, `UPDATE app_secret_refs SET stale=0 WHERE app_id=? AND service=?`, appID, svc); err != nil {
				return err
			}
		}
		return nil
	})
}
