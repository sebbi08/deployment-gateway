package admin

import (
	"errors"
	"net/http"
	"path/filepath"

	"github.com/sebbi08/deployment-gateway/internal/service"
	"github.com/sebbi08/deployment-gateway/internal/wsfiles"
)

// Read-only view of a workspace folder (compose files and app data). The
// data folders are written by agent containers, so all access goes through
// wsfiles, which never follows symlinks or opens special files.
func (s *Server) fileRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/admin/workspaces/{name}/files", s.listFiles)
	mux.HandleFunc("GET /api/admin/workspaces/{name}/files/content", s.fileContent)
}

func (s *Server) listFiles(w http.ResponseWriter, r *http.Request) {
	ws, ok := s.loadWorkspace(w, r)
	if !ok {
		return
	}
	l, err := wsfiles.List(filepath.Join(s.opts.Svc.Root, ws.Name), r.URL.Query().Get("path"))
	if err != nil {
		writeFileErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, l)
}

func (s *Server) fileContent(w http.ResponseWriter, r *http.Request) {
	ws, ok := s.loadWorkspace(w, r)
	if !ok {
		return
	}
	c, err := wsfiles.Read(filepath.Join(s.opts.Svc.Root, ws.Name), r.URL.Query().Get("path"))
	if err != nil {
		writeFileErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, c)
}

func writeFileErr(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, wsfiles.ErrNotFound):
		notFound(w, "path")
	case errors.Is(err, wsfiles.ErrInvalidPath), errors.Is(err, wsfiles.ErrSymlink),
		errors.Is(err, wsfiles.ErrNotDir), errors.Is(err, wsfiles.ErrNotFile):
		writeJSON(w, http.StatusUnprocessableEntity, apiError{Code: service.CodeValidation, Message: err.Error()})
	default:
		writeErr(w, err)
	}
}
