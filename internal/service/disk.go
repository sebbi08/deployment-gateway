package service

import (
	"io/fs"
	"path/filepath"
	"sync"
	"time"
)

type diskEntry struct {
	bytes int64
	at    time.Time
}

// diskCache measures directory sizes at most once per ttl.
type diskCache struct {
	mu  sync.Mutex
	ttl time.Duration
	m   map[string]diskEntry
}

func newDiskCache(ttl time.Duration) *diskCache {
	return &diskCache{ttl: ttl, m: map[string]diskEntry{}}
}

func (d *diskCache) usage(dir string) int64 {
	d.mu.Lock()
	e, ok := d.m[dir]
	d.mu.Unlock()
	if ok && time.Since(e.at) < d.ttl {
		return e.bytes
	}
	var total int64
	filepath.WalkDir(dir, func(_ string, de fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if de.Type().IsRegular() {
			if info, err := de.Info(); err == nil {
				total += info.Size()
			}
		}
		return nil
	})
	d.mu.Lock()
	d.m[dir] = diskEntry{bytes: total, at: time.Now()}
	d.mu.Unlock()
	return total
}
