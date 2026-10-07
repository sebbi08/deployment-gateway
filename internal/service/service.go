// Package service orchestrates every agent and admin operation. It is the
// only package that combines validation, rendering, verification, compose
// execution, runtime checks, persistence and auditing.
package service

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/sebbi08/deployment-gateway/internal/dockerx"
	"github.com/sebbi08/deployment-gateway/internal/store"
)

type Compose interface {
	Pull(ctx context.Context, file, project string) (string, error)
	Up(ctx context.Context, file, project string) (string, error)
	Down(ctx context.Context, file, project string) (string, error)
	Restart(ctx context.Context, file, project, service string) (string, error)
	Stop(ctx context.Context, file, project, service string) (string, error)
	TryLock(key string) (func(), bool)
}

type Docker interface {
	ProjectContainers(ctx context.Context, ws, app string) ([]dockerx.ContainerInfo, error)
	ManagedContainers(ctx context.Context) ([]dockerx.ContainerInfo, error)
	WorkspaceContainers(ctx context.Context, ws string) ([]dockerx.ContainerInfo, error)
	Inspect(ctx context.Context, id string) (dockerx.Facts, error)
	Logs(ctx context.Context, id string, tail int, since string, maxBytes int) (string, error)
	ImageLabels(ctx context.Context, ref string) (map[string]string, error)
	NetworkExists(ctx context.Context, name string) (bool, error)
	RemoveContainer(ctx context.Context, id string) error
	RemoveVolume(ctx context.Context, name string) error
}

// Principal is the authenticated caller. Workspace comes from the token
// lookup only — never from agent input.
type Principal struct {
	Workspace store.Workspace
	TokenID   int64
	// TokenHash identifies the token beyond its (reusable) ID.
	TokenHash string
	ClientIP  string
}

type Service struct {
	Store   *store.Store
	Compose Compose
	Docker  Docker
	Root    string
	Chown   func(path string, uid, gid int) error
	// FileOwner, if set, owns gateway-managed folders and compose files
	// (FILE_OWNER). Service data dirs keep the container user's ownership.
	FileOwner *Owner
	Logger    *slog.Logger

	wsLocks sync.Map // workspace name -> *sync.Mutex
	disk    *diskCache
}

func New(st *store.Store, c Compose, d Docker, root string) *Service {
	return &Service{Store: st, Compose: c, Docker: d, Root: root, Chown: os.Chown,
		Logger: slog.Default(), disk: newDiskCache(10 * time.Minute)}
}

// Owner is a numeric uid/gid pair.
type Owner struct {
	UID int
	GID int
}

// own applies FileOwner to gateway-managed paths; a no-op when unset.
func (s *Service) own(paths ...string) error {
	if s.FileOwner == nil {
		return nil
	}
	for _, p := range paths {
		if err := s.Chown(p, s.FileOwner.UID, s.FileOwner.GID); err != nil {
			return fmt.Errorf("chown %s: %w", p, err)
		}
	}
	return nil
}

func (s *Service) workspaceDir(ws string) string { return filepath.Join(s.Root, ws) }
func (s *Service) appDir(ws, app string) string  { return filepath.Join(s.Root, ws, app) }
func (s *Service) dataDir(ws, app string) string { return filepath.Join(s.Root, ws, app, "data") }
func (s *Service) composeFile(ws, app string) string {
	return filepath.Join(s.appDir(ws, app), "docker-compose.yml")
}
func (s *Service) prevFile(ws, app string) string {
	return filepath.Join(s.appDir(ws, app), "docker-compose.prev.yml")
}
func lockKey(ws, app string) string { return ws + "/" + app }

// wsMutex serialises quota checks and state changes within a workspace.
func (s *Service) wsMutex(ws string) *sync.Mutex {
	m, _ := s.wsLocks.LoadOrStore(ws, &sync.Mutex{})
	return m.(*sync.Mutex)
}

// current reloads the caller's workspace and checks that the token is still
// valid. Workspace and token IDs are reused after deletion, so the workspace
// must also keep its name and creation time, and the token its hash.
func (s *Service) current(ctx context.Context, p Principal) (store.Workspace, error) {
	ws, err := s.Store.GetWorkspace(ctx, p.Workspace.ID)
	if errors.Is(err, store.ErrNotFound) || (err == nil && (ws.Name != p.Workspace.Name || !ws.CreatedAt.Equal(p.Workspace.CreatedAt))) {
		return store.Workspace{}, notFoundError("workspace")
	}
	if err != nil {
		return store.Workspace{}, err
	}
	ok, err := s.Store.TokenActive(ctx, p.TokenID, p.TokenHash, ws.ID)
	if err != nil {
		return store.Workspace{}, err
	}
	if !ok {
		return store.Workspace{}, &Error{Code: CodeUnauthorized, Message: "unauthorized"}
	}
	return ws, nil
}
