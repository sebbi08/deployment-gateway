package service

import (
	"context"
	"maps"
	"slices"

	"github.com/sebbi08/deployment-gateway/internal/dockerx"
)

// Anonymous volumes come from an image's VOLUME instruction. compose
// re-mounts them by name when it recreates a container, and "down -v" and
// forced container removal then skip them, so the gateway removes them
// itself once no container of the app mounts them any more.

// anonymousVolumes returns the anonymous volumes mounted by the containers.
func (s *Service) anonymousVolumes(ctx context.Context, ids []string) map[string]bool {
	out := map[string]bool{}
	for _, id := range ids {
		f, err := s.Docker.Inspect(ctx, id)
		if err != nil {
			continue
		}
		for _, m := range f.Mounts {
			if m.Type == "volume" && dockerx.IsAnonymousVolume(m.Name) {
				out[m.Name] = true
			}
		}
	}
	return out
}

// appVolumes returns the anonymous volumes mounted by the app's containers.
func (s *Service) appVolumes(ctx context.Context, ws, app string) map[string]bool {
	containers, err := s.Docker.ProjectContainers(ctx, ws, app)
	if err != nil {
		return map[string]bool{}
	}
	ids := make([]string, 0, len(containers))
	for _, c := range containers {
		ids = append(ids, c.ID)
	}
	return s.anonymousVolumes(ctx, ids)
}

// removeVolumes removes every volume in vols that is not in keep. Docker
// refuses to remove a volume that is still in use; such failures are logged.
func (s *Service) removeVolumes(ctx context.Context, vols, keep map[string]bool) {
	for _, v := range slices.Sorted(maps.Keys(vols)) {
		if keep[v] {
			continue
		}
		if err := s.Docker.RemoveVolume(ctx, v); err != nil {
			s.Logger.Warn("removing anonymous volume failed", "volume", v, "err", err)
		}
	}
}
