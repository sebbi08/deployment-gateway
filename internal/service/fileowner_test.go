package service_test

import (
	"slices"
	"strings"
	"testing"

	"github.com/sebbi08/deployment-gateway/internal/service"
)

// FILE_OWNER grants its group read access only: the folders and compose
// files stay owned by root, because the root gateway runs those files and
// writes into those folders; an owning host user could swap in a compose
// file or plant symlinks and become root.
func TestFileOwnerGrantsGroupReadOnly(t *testing.T) {
	h := newHarness(t)
	h.svc.FileOwner = &service.Owner{UID: 1000, GID: 1001}
	p := h.workspace("alice", 20000, 20099)
	if !slices.Contains(h.chowns, "alice 0:1001") {
		t.Fatalf("workspace folder not chowned on create: %v", h.chowns)
	}
	h.chowns = nil
	deployExample(t, h, p)
	for _, want := range []string{"alice 0:1001", "shop 0:1001", "data 0:1001", "docker-compose.next.yml 0:1001", "db 999:999"} {
		if !slices.Contains(h.chowns, want) {
			t.Errorf("missing chown %q in %v", want, h.chowns)
		}
	}
	// Service data dirs keep the container user (db) or root (web has no volume here).
	for _, c := range h.chowns {
		if strings.HasPrefix(c, "db ") && c != "db 999:999" {
			t.Errorf("service data dir must not get FILE_OWNER: %v", h.chowns)
		}
	}
	// A second successful deploy rotates the compose file into prev, which is chowned too.
	h.chowns = nil
	deployExample(t, h, p)
	if !slices.Contains(h.chowns, "docker-compose.prev.yml 0:1001") {
		t.Fatalf("prev file not chowned: %v", h.chowns)
	}
}

func TestFileOwnerUnsetChownsNothingExtra(t *testing.T) {
	h := newHarness(t)
	p := h.workspace("alice", 20000, 20099)
	deployExample(t, h, p)
	if !slices.Equal(h.chowns, []string{"db 999:999"}) {
		t.Fatalf("chowns = %v", h.chowns)
	}
}
