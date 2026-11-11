package wsfiles

import (
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

// newTree builds a small workspace folder:
//
//	shop/docker-compose.yml
//	shop/data/db/PG_VERSION
//	outside/secret   (a sibling of the workspace, i.e. outside of it)
func newTree(t *testing.T) (ws, outside string) {
	t.Helper()
	base := t.TempDir()
	ws = filepath.Join(base, "alice")
	outside = filepath.Join(base, "outside")
	mustMkdir(t, filepath.Join(ws, "shop", "data", "db"))
	mustMkdir(t, outside)
	mustWrite(t, filepath.Join(ws, "shop", "docker-compose.yml"), "services:\n  web:\n    image: x:1\n")
	mustWrite(t, filepath.Join(ws, "shop", "data", "db", "PG_VERSION"), "17\n")
	mustWrite(t, filepath.Join(outside, "secret"), "TOP-SECRET")
	return ws, outside
}

func mustMkdir(t *testing.T, p string) {
	t.Helper()
	if err := os.MkdirAll(p, 0o755); err != nil {
		t.Fatal(err)
	}
}

func mustWrite(t *testing.T, p, s string) {
	t.Helper()
	if err := os.WriteFile(p, []byte(s), 0o644); err != nil {
		t.Fatal(err)
	}
}

func mustSymlink(t *testing.T, target, link string) {
	t.Helper()
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
}

func entry(l Listing, name string) (Entry, bool) {
	for _, e := range l.Entries {
		if e.Name == name {
			return e, true
		}
	}
	return Entry{}, false
}

func TestListRootShowsAppFolders(t *testing.T) {
	ws, _ := newTree(t)
	l, err := List(ws, "")
	if err != nil {
		t.Fatal(err)
	}
	if l.Path != "" || len(l.Entries) != 1 || l.Entries[0].Name != "shop" || l.Entries[0].Type != TypeDir {
		t.Fatalf("listing = %+v", l)
	}
}

func TestListSortsDirsFirstAndReportsSizes(t *testing.T) {
	ws, _ := newTree(t)
	mustWrite(t, filepath.Join(ws, "shop", ".prev.yml"), "old")
	l, err := List(ws, "shop")
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, e := range l.Entries {
		names = append(names, e.Name)
	}
	if got := strings.Join(names, ","); got != "data,.prev.yml,docker-compose.yml" {
		t.Fatalf("order = %s", got)
	}
	e, _ := entry(l, ".prev.yml")
	if e.Type != TypeFile || e.Size != 3 || e.ModTime.IsZero() {
		t.Fatalf(".prev.yml = %+v", e)
	}
}

func TestListShowsSymlinkWithoutFollowing(t *testing.T) {
	ws, outside := newTree(t)
	mustSymlink(t, filepath.Join(outside, "secret"), filepath.Join(ws, "shop", "data", "db", "evil"))
	mustSymlink(t, outside, filepath.Join(ws, "shop", "data", "db", "evildir"))
	l, err := List(ws, "shop/data/db")
	if err != nil {
		t.Fatal(err)
	}
	e, ok := entry(l, "evil")
	if !ok || e.Type != TypeSymlink || e.Target != filepath.Join(outside, "secret") {
		t.Fatalf("evil = %+v", e)
	}
	if e.Size == int64(len("TOP-SECRET")) {
		t.Fatal("size of the link target leaked")
	}
	if d, _ := entry(l, "evildir"); d.Type != TypeSymlink {
		t.Fatalf("evildir = %+v", d)
	}
}

func TestListRefusesSymlinkedDirectories(t *testing.T) {
	ws, outside := newTree(t)
	mustSymlink(t, outside, filepath.Join(ws, "shop", "data", "out"))
	mustSymlink(t, filepath.Join(ws, "shop"), filepath.Join(ws, "shop", "data", "in"))
	for _, p := range []string{"shop/data/out", "shop/data/in", "shop/data/in/data"} {
		if _, err := List(ws, p); !errors.Is(err, ErrSymlink) {
			t.Errorf("List(%q) err = %v, want ErrSymlink", p, err)
		}
	}
}

func TestListOnFileIsNotDir(t *testing.T) {
	ws, _ := newTree(t)
	if _, err := List(ws, "shop/docker-compose.yml"); !errors.Is(err, ErrNotDir) {
		t.Fatalf("err = %v", err)
	}
}

func TestListCapsEntries(t *testing.T) {
	ws, _ := newTree(t)
	dir := filepath.Join(ws, "shop", "data", "many")
	mustMkdir(t, dir)
	for i := range MaxEntries + 7 {
		mustWrite(t, filepath.Join(dir, "f"+strconv.Itoa(i)), "")
	}
	l, err := List(ws, "shop/data/many")
	if err != nil {
		t.Fatal(err)
	}
	if len(l.Entries) != MaxEntries || l.More != 7 {
		t.Fatalf("entries = %d, more = %d", len(l.Entries), l.More)
	}
}

func TestListShowsSpecialFilesByType(t *testing.T) {
	ws, _ := newTree(t)
	if err := syscall.Mkfifo(filepath.Join(ws, "shop", "data", "pipe"), 0o644); err != nil {
		t.Fatal(err)
	}
	l, err := List(ws, "shop/data")
	if err != nil {
		t.Fatal(err)
	}
	if e, _ := entry(l, "pipe"); e.Type != TypeFIFO {
		t.Fatalf("pipe = %+v", e)
	}
}

func TestInvalidPaths(t *testing.T) {
	ws, _ := newTree(t)
	for _, p := range []string{"/etc", "..", "../outside", "shop/../..", "shop//data", "./shop", "shop/", "shop/.", "a\x00b", strings.Repeat("a/", 3000) + "a"} {
		if _, err := List(ws, p); !errors.Is(err, ErrInvalidPath) {
			t.Errorf("List(%q) err = %v", p, err)
		}
		if _, err := Read(ws, p); !errors.Is(err, ErrInvalidPath) {
			t.Errorf("Read(%q) err = %v", p, err)
		}
	}
}

func TestNotFound(t *testing.T) {
	ws, _ := newTree(t)
	if _, err := List(ws, "nope"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("List err = %v", err)
	}
	if _, err := Read(ws, "shop/nope.yml"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("Read err = %v", err)
	}
}

func TestReadTextFile(t *testing.T) {
	ws, _ := newTree(t)
	c, err := Read(ws, "shop/docker-compose.yml")
	if err != nil {
		t.Fatal(err)
	}
	if c.Path != "shop/docker-compose.yml" || c.Binary || c.Truncated || c.Text != "services:\n  web:\n    image: x:1\n" || c.Size != int64(len(c.Text)) {
		t.Fatalf("content = %+v", c)
	}
}

func TestReadRefusesSymlinks(t *testing.T) {
	ws, outside := newTree(t)
	mustSymlink(t, filepath.Join(outside, "secret"), filepath.Join(ws, "shop", "data", "evil"))
	mustSymlink(t, outside, filepath.Join(ws, "shop", "data", "evildir"))
	mustSymlink(t, "../docker-compose.yml", filepath.Join(ws, "shop", "data", "inside"))
	if _, err := Read(ws, "shop/data/evil"); !errors.Is(err, ErrSymlink) {
		t.Errorf("file link err = %v", err)
	}
	if _, err := Read(ws, "shop/data/evildir/secret"); !errors.Is(err, ErrSymlink) {
		t.Errorf("dir link err = %v", err)
	}
	if _, err := Read(ws, "shop/data/inside"); !errors.Is(err, ErrSymlink) {
		t.Errorf("inside link err = %v", err)
	}
}

func TestReadDirectoryIsNotFile(t *testing.T) {
	ws, _ := newTree(t)
	if _, err := Read(ws, "shop/data"); !errors.Is(err, ErrNotFile) {
		t.Fatalf("err = %v", err)
	}
}

func TestReadFIFODoesNotBlock(t *testing.T) {
	ws, _ := newTree(t)
	if err := syscall.Mkfifo(filepath.Join(ws, "shop", "data", "pipe"), 0o644); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() {
		_, err := Read(ws, "shop/data/pipe")
		done <- err
	}()
	select {
	case err := <-done:
		if !errors.Is(err, ErrNotFile) {
			t.Fatalf("err = %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("Read blocked on a FIFO")
	}
}

func TestReadTruncatesLargeFiles(t *testing.T) {
	ws, _ := newTree(t)
	mustWrite(t, filepath.Join(ws, "shop", "data", "big.log"), strings.Repeat("x", MaxContent+100))
	c, err := Read(ws, "shop/data/big.log")
	if err != nil {
		t.Fatal(err)
	}
	if !c.Truncated || c.Binary || len(c.Text) != MaxContent || c.Size != MaxContent+100 {
		t.Fatalf("truncated=%v binary=%v len=%d size=%d", c.Truncated, c.Binary, len(c.Text), c.Size)
	}
}

func TestReadTruncationInsideMultibyteRuneStaysText(t *testing.T) {
	ws, _ := newTree(t)
	// "ä" is two bytes; MaxContent-1 ASCII bytes put the cut between them.
	mustWrite(t, filepath.Join(ws, "shop", "data", "u.txt"), strings.Repeat("a", MaxContent-1)+"äää")
	c, err := Read(ws, "shop/data/u.txt")
	if err != nil {
		t.Fatal(err)
	}
	if c.Binary || !c.Truncated || len(c.Text) != MaxContent-1 {
		t.Fatalf("binary=%v truncated=%v len=%d", c.Binary, c.Truncated, len(c.Text))
	}
}

func TestReadBinaryFiles(t *testing.T) {
	ws, _ := newTree(t)
	mustWrite(t, filepath.Join(ws, "shop", "data", "nul.bin"), "abc\x00def")
	mustWrite(t, filepath.Join(ws, "shop", "data", "latin1.txt"), "caf\xe9")
	for _, p := range []string{"shop/data/nul.bin", "shop/data/latin1.txt"} {
		c, err := Read(ws, p)
		if err != nil {
			t.Fatal(err)
		}
		if !c.Binary || c.Text != "" || c.Size == 0 {
			t.Errorf("%s = %+v", p, c)
		}
	}
}
