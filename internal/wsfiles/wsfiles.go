// Package wsfiles gives the admin UI a read-only view of a workspace folder.
//
// The data/ folders below a workspace are written by agent-controlled
// containers, while the gateway runs as root. Everything here therefore
// walks paths one component at a time with openat(O_NOFOLLOW) relative to
// the parent directory's fd: no symlink is ever followed (not even one that
// stays inside the workspace), ".." is rejected, and a file's type is
// checked both before and after opening it, so special files are never
// opened and a swap between the checks is caught.
package wsfiles

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"slices"
	"strings"
	"time"
	"unicode/utf8"

	"golang.org/x/sys/unix"
)

const (
	// MaxEntries is the number of entries one listing returns.
	MaxEntries = 500
	// MaxContent is the number of bytes Read returns.
	MaxContent = 512 << 10

	// maxScan bounds the names a listing keeps in memory for sorting;
	// names beyond it are only counted.
	maxScan    = 10000
	maxPathLen = 4096
)

// Entry types.
const (
	TypeDir     = "dir"
	TypeFile    = "file"
	TypeSymlink = "symlink"
	TypeFIFO    = "fifo"
	TypeSocket  = "socket"
	TypeDevice  = "device"
	TypeOther   = "other"
)

var (
	ErrInvalidPath = errors.New("invalid path")
	ErrNotFound    = errors.New("not found")
	ErrSymlink     = errors.New("path contains a symlink")
	ErrNotDir      = errors.New("not a directory")
	ErrNotFile     = errors.New("not a regular file")
)

type Entry struct {
	Name    string    `json:"name"`
	Type    string    `json:"type"`
	Size    int64     `json:"size"`
	ModTime time.Time `json:"mod_time"`
	// Target is a symlink's target text; it is never resolved.
	Target string `json:"target,omitempty"`
}

type Listing struct {
	Path    string  `json:"path"`
	Entries []Entry `json:"entries"`
	// More counts entries left out because of MaxEntries.
	More int `json:"more"`
}

type Content struct {
	Path      string    `json:"path"`
	Size      int64     `json:"size"`
	ModTime   time.Time `json:"mod_time"`
	Binary    bool      `json:"binary"`
	Truncated bool      `json:"truncated"`
	// Text is empty for binary files.
	Text string `json:"text"`
}

// List returns the entries of directory rel ("" = the workspace folder
// root), directories first, then by name.
func List(root, rel string) (Listing, error) {
	parts, err := splitPath(rel)
	if err != nil {
		return Listing{}, err
	}
	fd, err := walk(root, parts)
	if err != nil {
		return Listing{}, err
	}
	dir := os.NewFile(uintptr(fd), rel)
	defer dir.Close()

	type name struct {
		name  string
		isDir bool
	}
	var names []name
	total := 0
	for {
		batch, err := dir.ReadDir(1000)
		for _, de := range batch {
			total++
			if len(names) < maxScan {
				names = append(names, name{de.Name(), de.IsDir()})
			}
		}
		if err == io.EOF || len(batch) == 0 {
			break
		}
		if err != nil {
			return Listing{}, fmt.Errorf("read dir: %w", err)
		}
	}
	slices.SortFunc(names, func(a, b name) int {
		if a.isDir != b.isDir {
			if a.isDir {
				return -1
			}
			return 1
		}
		return strings.Compare(a.name, b.name)
	})
	if len(names) > MaxEntries {
		names = names[:MaxEntries]
	}

	l := Listing{Path: rel, Entries: []Entry{}}
	for _, n := range names {
		var st unix.Stat_t
		if err := unix.Fstatat(fd, n.name, &st, unix.AT_SYMLINK_NOFOLLOW); err != nil {
			continue // vanished since ReadDir
		}
		e := Entry{Name: n.name, Type: typeOf(st.Mode), Size: st.Size, ModTime: mtime(st)}
		if e.Type == TypeSymlink {
			e.Target = readlink(fd, n.name)
		}
		l.Entries = append(l.Entries, e)
	}
	l.More = total - len(names)
	return l, nil
}

// Read returns up to MaxContent bytes of regular file rel.
func Read(root, rel string) (Content, error) {
	parts, err := splitPath(rel)
	if err != nil {
		return Content{}, err
	}
	if len(parts) == 0 {
		return Content{}, ErrNotFile
	}
	dirfd, err := walk(root, parts[:len(parts)-1])
	if err != nil {
		return Content{}, err
	}
	fd, err := openAt(dirfd, parts[len(parts)-1], unix.S_IFREG)
	unix.Close(dirfd)
	if err != nil {
		return Content{}, err
	}
	f := os.NewFile(uintptr(fd), rel)
	defer f.Close()
	var st unix.Stat_t
	if err := unix.Fstat(fd, &st); err != nil {
		return Content{}, fmt.Errorf("stat: %w", err)
	}
	buf, err := io.ReadAll(io.LimitReader(f, MaxContent+1))
	if err != nil {
		return Content{}, fmt.Errorf("read: %w", err)
	}
	c := Content{Path: rel, Size: st.Size, ModTime: mtime(st)}
	if len(buf) > MaxContent {
		buf = buf[:MaxContent]
		c.Truncated = true
	}
	text, ok := asText(buf, c.Truncated)
	c.Binary = !ok
	c.Text = text
	return c, nil
}

// asText reports whether buf is UTF-8 text without NUL bytes. A truncated
// buf may end inside a multi-byte rune; that partial rune is dropped.
func asText(buf []byte, truncated bool) (string, bool) {
	if bytes.IndexByte(buf, 0) >= 0 {
		return "", false
	}
	cut := 0
	if truncated {
		cut = utf8.UTFMax - 1
	}
	for k := 0; k <= cut && k <= len(buf); k++ {
		if utf8.Valid(buf[:len(buf)-k]) {
			return string(buf[:len(buf)-k]), true
		}
	}
	return "", false
}

func splitPath(rel string) ([]string, error) {
	if rel == "" {
		return nil, nil
	}
	if len(rel) > maxPathLen || strings.ContainsRune(rel, 0) {
		return nil, ErrInvalidPath
	}
	parts := strings.Split(rel, "/")
	for _, p := range parts {
		if p == "" || p == "." || p == ".." {
			return nil, ErrInvalidPath
		}
	}
	return parts, nil
}

// walk opens root and then each of parts as a directory, never following
// a symlink. The caller closes the returned fd.
func walk(root string, parts []string) (int, error) {
	fd, err := unix.Open(root, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC, 0)
	if err != nil {
		return -1, mapErr(err)
	}
	for _, p := range parts {
		next, err := openAt(fd, p, unix.S_IFDIR)
		unix.Close(fd)
		if err != nil {
			return -1, err
		}
		fd = next
	}
	return fd, nil
}

// openAt opens name inside dir if it is of type want (S_IFDIR or S_IFREG).
// The type is checked with lstat before opening, so special files are
// never opened, and with fstat afterwards, so a swap in between is caught.
// O_NONBLOCK keeps a FIFO swapped in between from blocking the open.
func openAt(dir int, name string, want uint32) (int, error) {
	var st unix.Stat_t
	if err := unix.Fstatat(dir, name, &st, unix.AT_SYMLINK_NOFOLLOW); err != nil {
		return -1, mapErr(err)
	}
	if err := checkType(st.Mode, want); err != nil {
		return -1, err
	}
	flags := unix.O_RDONLY | unix.O_NOFOLLOW | unix.O_NONBLOCK | unix.O_CLOEXEC
	if want == unix.S_IFDIR {
		flags |= unix.O_DIRECTORY
	}
	fd, err := unix.Openat(dir, name, flags, 0)
	if err != nil {
		return -1, mapErr(err)
	}
	if err := unix.Fstat(fd, &st); err != nil {
		unix.Close(fd)
		return -1, mapErr(err)
	}
	if err := checkType(st.Mode, want); err != nil {
		unix.Close(fd)
		return -1, err
	}
	return fd, nil
}

func checkType(mode uint32, want uint32) error {
	switch mode & unix.S_IFMT {
	case want:
		return nil
	case unix.S_IFLNK:
		return ErrSymlink
	}
	if want == unix.S_IFDIR {
		return ErrNotDir
	}
	return ErrNotFile
}

func mapErr(err error) error {
	switch {
	case errors.Is(err, unix.ENOENT):
		return ErrNotFound
	case errors.Is(err, unix.ELOOP):
		return ErrSymlink
	case errors.Is(err, unix.ENOTDIR):
		return ErrNotDir
	}
	return err
}

func typeOf(mode uint32) string {
	switch mode & unix.S_IFMT {
	case unix.S_IFDIR:
		return TypeDir
	case unix.S_IFREG:
		return TypeFile
	case unix.S_IFLNK:
		return TypeSymlink
	case unix.S_IFIFO:
		return TypeFIFO
	case unix.S_IFSOCK:
		return TypeSocket
	case unix.S_IFCHR, unix.S_IFBLK:
		return TypeDevice
	}
	return TypeOther
}

func mtime(st unix.Stat_t) time.Time {
	return time.Unix(st.Mtim.Unix()).UTC()
}

func readlink(dir int, name string) string {
	buf := make([]byte, maxPathLen)
	n, err := unix.Readlinkat(dir, name, buf)
	if err != nil {
		return ""
	}
	return string(buf[:n])
}
