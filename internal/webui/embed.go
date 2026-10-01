// Package webui embeds the built React admin app (web/ → internal/webui/dist).
package webui

import (
	"embed"
	"io/fs"
)

// dist always contains .keep so the package compiles before the first
// frontend build; the admin server reports "admin UI not built" until
// index.html exists.
//
//go:embed all:dist
var dist embed.FS

// FS returns the build output root.
func FS() fs.FS {
	sub, err := fs.Sub(dist, "dist")
	if err != nil {
		panic(err)
	}
	return sub
}
