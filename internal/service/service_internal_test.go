package service

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/sebbi08/deployment-gateway/internal/store"
)

// The gateway never follows symlinks when changing ownership.
func TestDefaultChownDoesNotFollowSymlinks(t *testing.T) {
	s := New(nil, nil, nil, t.TempDir())
	if reflect.ValueOf(s.Chown).Pointer() != reflect.ValueOf(os.Lchown).Pointer() {
		t.Fatal("default Chown must be os.Lchown")
	}
}

// UpdateWorkspace checks port claims and writes the new range; the deploy
// commit adds claims under the workspace mutex, so the update must hold it
// too or a claim can end up outside the stored range.
func TestUpdateWorkspaceHoldsWorkspaceMutex(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "s.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	s := New(st, nil, nil, t.TempDir())
	s.Chown = func(string, int, int) error { return nil }
	ctx := context.Background()
	w, err := s.CreateWorkspace(ctx, "admin", "alice", store.BuiltinDefaultQuota(), PortSettings{Start: 20000, End: 20099, BindIP: "0.0.0.0"})
	if err != nil {
		t.Fatal(err)
	}
	mu := s.wsMutex("alice")
	mu.Lock()
	done := make(chan error, 1)
	go func() { done <- s.UpdateWorkspace(ctx, "admin", w) }()
	select {
	case <-done:
		t.Fatal("UpdateWorkspace ran while the workspace mutex was held")
	case <-time.After(100 * time.Millisecond):
	}
	mu.Unlock()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}

// Port-range overlap and template suffix checks compare with other rows and
// then write; concurrent admin requests must be serialised.
func TestAdminUniquenessChecksAreSerialised(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "s.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	s := New(st, nil, nil, t.TempDir())
	s.Chown = func(string, int, int) error { return nil }
	ctx := context.Background()
	s.adminMu.Lock()
	done := make(chan error, 1)
	go func() {
		_, err := s.CreateWorkspace(ctx, "admin", "alice", store.BuiltinDefaultQuota(), PortSettings{Start: 20000, End: 20099, BindIP: "0.0.0.0"})
		done <- err
	}()
	select {
	case <-done:
		t.Fatal("CreateWorkspace ran while the admin mutex was held")
	case <-time.After(100 * time.Millisecond):
	}
	s.adminMu.Unlock()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}

func TestWriteAndCopyFileSync(t *testing.T) {
	dir := t.TempDir()
	a, b := filepath.Join(dir, "a"), filepath.Join(dir, "b")
	if err := writeFileSync(a, []byte("services: {}\n"), 0o640); err != nil {
		t.Fatal(err)
	}
	if err := copyFileSync(a, b, 0o640); err != nil {
		t.Fatal(err)
	}
	for _, p := range []string{a, b} {
		got, err := os.ReadFile(p)
		fi, _ := os.Stat(p)
		if err != nil || string(got) != "services: {}\n" || fi.Mode().Perm() != 0o640 {
			t.Fatalf("%s: %q %v %v", p, got, fi.Mode().Perm(), err)
		}
	}
}
