package service_test

import (
	"os"
	"path/filepath"
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

// A root container can create setuid-root files in its data folder despite
// cap_drop ALL. Root-owned data folders are therefore closed to every other
// host user; folders owned by the container's user keep 0755.
func TestRootOwnedDataFolderIsPrivate(t *testing.T) {
	h := newHarness(t)
	p := h.workspace("alice", 20000, 20099)
	st := `{"services": {
	  "root": {"image": "a:1", "volume": {"path": "/data"}},
	  "zero": {"image": "a:1", "user": "00:0", "volume": {"path": "/data"}},
	  "db": {"image": "postgres:16.4", "user": "999:999", "volume": {"path": "/var/lib/postgresql/data"}}
	}}`
	if _, err := h.svc.Deploy(h.ctx, p, service.DeployInput{App: "shop", Stack: mustStack(t, st)}); err != nil {
		t.Fatal(err)
	}
	for svc, want := range map[string]os.FileMode{"root": 0o700, "zero": 0o700, "db": 0o755} {
		fi, err := os.Stat(filepath.Join(h.root, "alice", "shop", "data", svc))
		if err != nil {
			t.Fatal(err)
		}
		if got := fi.Mode().Perm(); got != want {
			t.Errorf("data/%s mode %o, want %o", svc, got, want)
		}
	}
}

// Without CAP_DAC_OVERRIDE a root container cannot write a folder owned by
// a previous `user`; dropping `user` hands the folder back to root.
func TestDroppingUserReturnsDataFolderToRoot(t *testing.T) {
	h := newHarness(t)
	p := h.workspace("alice", 20000, 20099)
	deployExample(t, h, p)
	h.chowns = nil
	noUser := strings.Replace(exampleStack, `"user": "999:999", `, "", 1)
	if _, err := h.svc.Deploy(h.ctx, p, service.DeployInput{App: "shop", Stack: mustStack(t, noUser)}); err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(h.chowns, "db 0:0") {
		t.Fatalf("data/db not returned to root: %v", h.chowns)
	}
}
