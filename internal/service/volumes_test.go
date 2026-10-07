package service_test

import (
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/sebbi08/deployment-gateway/internal/dockerx"
	"github.com/sebbi08/deployment-gateway/internal/service"
)

var (
	volX = strings.Repeat("a", 64)
	volY = strings.Repeat("b", 64)
)

// mountVolume gives a container an anonymous volume from an image VOLUME,
// as Docker does for e.g. postgres.
func mountVolume(h *harness, container, name string) {
	h.docker.MutateFacts(container, func(f *dockerx.Facts) {
		f.Mounts = append(f.Mounts, dockerx.MountFact{Type: "volume", Name: name, Destination: "/var/lib/data"})
	})
}

// compose re-mounts an anonymous volume by name when it recreates a
// container, and down -v then skips it; delete must remove it explicitly.
func TestDeleteRemovesAnonymousVolumes(t *testing.T) {
	h := newHarness(t)
	p := h.workspace("alice", 20000, 20099)
	deployExample(t, h, p)
	mountVolume(h, "alice-shop-db", volX)
	if _, err := h.svc.Delete(h.ctx, p, service.DeleteInput{App: "shop", Confirm: "shop"}); err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(h.docker.RemovedVolumes(), volX) {
		t.Fatalf("anonymous volume not removed: %v", h.docker.RemovedVolumes())
	}
}

func TestRedeployRemovesOnlyUnmountedAnonymousVolumes(t *testing.T) {
	h := newHarness(t)
	p := h.workspace("alice", 20000, 20099)
	deployExample(t, h, p)
	mountVolume(h, "alice-shop-web", volX)
	mountVolume(h, "alice-shop-db", volY)
	h.compose.AfterUp = func() { mountVolume(h, "alice-shop-db", volY) } // db keeps its volume
	deployExample(t, h, p)
	removed := h.docker.RemovedVolumes()
	if !slices.Contains(removed, volX) || slices.Contains(removed, volY) {
		t.Fatalf("removed %v, want only the unmounted %s", removed, volX[:8])
	}
}

func TestRemoveOrphanRemovesAnonymousVolumes(t *testing.T) {
	h := newHarness(t)
	p := h.workspace("alice", 20000, 20099)
	deployExample(t, h, p)
	mountVolume(h, "alice-shop-db", volX)
	if err := h.st.DeleteApp(h.ctx, mustApp(t, h, p, "shop")); err != nil { // the gateway lost track of the app
		t.Fatal(err)
	}
	if err := h.svc.RemoveOrphan(h.ctx, "admin", "alice-shop-db"); err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(h.docker.RemovedVolumes(), volX) {
		t.Fatalf("orphan's anonymous volume not removed: %v", h.docker.RemovedVolumes())
	}
}

func mustApp(t *testing.T, h *harness, p service.Principal, name string) int64 {
	t.Helper()
	a, err := h.st.GetApp(h.ctx, p.Workspace.ID, name)
	if err != nil {
		t.Fatal(err)
	}
	return a.ID
}

// Networks of removed apps hold one of Docker's limited address pools; the
// cleanup paths that bypass compose down must remove them too.
func TestCleanupRemovesAppNetwork(t *testing.T) {
	t.Run("delete after failing compose down", func(t *testing.T) {
		h := newHarness(t)
		p := h.workspace("alice", 20000, 20099)
		deployExample(t, h, p)
		h.compose.DownErr = errors.New("compose down failed")
		if _, err := h.svc.Delete(h.ctx, p, service.DeleteInput{App: "shop", Confirm: "shop"}); err != nil {
			t.Fatal(err)
		}
		if !slices.Contains(h.docker.RemovedNetworks(), "dg-alice-shop") {
			t.Fatalf("network not removed: %v", h.docker.RemovedNetworks())
		}
	})
	t.Run("orphan removal", func(t *testing.T) {
		h := newHarness(t)
		p := h.workspace("alice", 20000, 20099)
		deployExample(t, h, p)
		if err := h.st.DeleteApp(h.ctx, mustApp(t, h, p, "shop")); err != nil {
			t.Fatal(err)
		}
		for _, id := range []string{"alice-shop-web", "alice-shop-db"} {
			if err := h.svc.RemoveOrphan(h.ctx, "admin", id); err != nil {
				t.Fatal(err)
			}
		}
		if !slices.Contains(h.docker.RemovedNetworks(), "dg-alice-shop") {
			t.Fatalf("network not removed: %v", h.docker.RemovedNetworks())
		}
	})
}
