package service

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"time"

	"github.com/sebbi08/deployment-gateway/internal/names"
	"github.com/sebbi08/deployment-gateway/internal/store"
)

type composeOp func(ctx context.Context, file, project, service string) (string, error)

func (s *Service) Restart(ctx context.Context, p Principal, in ServiceInput) (res AppStatus, err error) {
	defer s.track(ctx, p, "restart", in, &err, time.Now())
	ws, err := s.current(ctx, p)
	if err != nil {
		return AppStatus{}, err
	}
	return s.lifecycle(ctx, ws, in.App, in.Service, s.Compose.Restart)
}

func (s *Service) Stop(ctx context.Context, p Principal, in ServiceInput) (res AppStatus, err error) {
	defer s.track(ctx, p, "stop", in, &err, time.Now())
	ws, err := s.current(ctx, p)
	if err != nil {
		return AppStatus{}, err
	}
	return s.lifecycle(ctx, ws, in.App, in.Service, s.Compose.Stop)
}

func (s *Service) lifecycle(ctx context.Context, ws store.Workspace, app, svc string, op composeOp) (AppStatus, error) {
	a, err := s.ownedApp(ctx, ws, app)
	if err != nil {
		return AppStatus{}, err
	}
	target := ""
	if svc != "" {
		if !names.ValidService(svc) {
			return AppStatus{}, notFoundError("service")
		}
		if _, err := s.findContainer(ctx, ws.Name, app, svc); err != nil {
			return AppStatus{}, err
		}
		target = names.Container(ws.Name, app, svc)
	}
	unlock, ok := s.Compose.TryLock(lockKey(ws.Name, app))
	if !ok {
		return AppStatus{}, &Error{Code: CodeConflict, Message: "another operation on this app is in progress"}
	}
	defer unlock()
	// Detached so a client disconnect cannot abort compose half-way; the
	// runner enforces its own timeout and the app lock is held meanwhile.
	if out, err := op(context.WithoutCancel(ctx), s.composeFile(ws.Name, app), names.Project(ws.Name, app), target); err != nil {
		return AppStatus{}, &Error{Code: CodeDeployFailed, Message: err.Error() + "\n" + s.sanitize(ws.Name, out), detail: out}
	}
	claims, err := s.Store.ListPortClaims(ctx, ws.ID)
	if err != nil {
		return AppStatus{}, err
	}
	var appClaims []store.PortClaim
	for _, c := range claims {
		if c.App == app {
			appClaims = append(appClaims, c)
		}
	}
	return s.status(ctx, ws, app, a.Hosts, appClaims, nil)
}

func (s *Service) Delete(ctx context.Context, p Principal, in DeleteInput) (res DeleteResult, err error) {
	defer s.track(ctx, p, "delete", in, &err, time.Now())
	if in.Confirm != in.App {
		return DeleteResult{}, validationError("confirm", "confirm must equal the app name")
	}
	ws, err := s.current(ctx, p)
	if err != nil {
		return DeleteResult{}, err
	}
	if err := s.deleteApp(ctx, ws, in.App); err != nil {
		return DeleteResult{}, err
	}
	return DeleteResult{App: in.App, Deleted: true}, nil
}

// deleteApp stops and removes containers, the private network, anonymous
// volumes, the app folder (including data) and the DB record. The
// irreversible part runs detached from request cancellation so a cancelled
// request cannot leave a half-deleted app.
func (s *Service) deleteApp(ctx context.Context, ws store.Workspace, app string) error {
	a, err := s.ownedApp(ctx, ws, app)
	if err != nil {
		return err
	}
	unlock, ok := s.Compose.TryLock(lockKey(ws.Name, app))
	if !ok {
		return &Error{Code: CodeConflict, Message: "another operation on this app is in progress"}
	}
	defer unlock()
	ctx = context.WithoutCancel(ctx)
	vols := s.appVolumes(ctx, ws.Name, app)
	file := s.composeFile(ws.Name, app)
	if _, statErr := os.Stat(file); statErr == nil {
		if out, err := s.Compose.Down(ctx, file, names.Project(ws.Name, app)); err != nil {
			return &Error{Code: CodeDeployFailed, Message: err.Error() + "\n" + s.sanitize(ws.Name, out), detail: out}
		}
	}
	leftovers, err := s.Docker.ProjectContainers(ctx, ws.Name, app)
	if err != nil {
		return err
	}
	for _, c := range leftovers {
		if err := s.Docker.RemoveContainer(ctx, c.ID); err != nil {
			return err
		}
	}
	s.removeVolumes(ctx, vols, nil)
	dir := s.appDir(ws.Name, app)
	if filepath.Dir(dir) != s.workspaceDir(ws.Name) {
		return errors.New("refusing to remove unexpected path " + dir)
	}
	if err := os.RemoveAll(dir); err != nil {
		return err
	}
	return s.Store.DeleteApp(ctx, a.ID)
}
