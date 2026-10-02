package admin

import (
	"net/http"
	"path/filepath"
	"strconv"

	"github.com/sebbi08/deployment-gateway/internal/service"
	"github.com/sebbi08/deployment-gateway/internal/store"
)

func (s *Server) workspaceRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/admin/dashboard", s.dashboard)
	mux.HandleFunc("GET /api/admin/workspaces", s.listWorkspaces)
	mux.HandleFunc("POST /api/admin/workspaces", s.createWorkspace)
	mux.HandleFunc("GET /api/admin/workspaces/{name}", s.getWorkspace)
	mux.HandleFunc("PUT /api/admin/workspaces/{name}", s.updateWorkspace)
	mux.HandleFunc("DELETE /api/admin/workspaces/{name}", s.deleteWorkspace)
	mux.HandleFunc("PUT /api/admin/workspaces/{name}/grants", s.setGrants)
	mux.HandleFunc("POST /api/admin/workspaces/{name}/tokens", s.createToken)
	mux.HandleFunc("POST /api/admin/workspaces/{name}/tokens/{id}/revoke", s.revokeToken)
	mux.HandleFunc("POST /api/admin/workspaces/{name}/apps/{app}/stop", s.stopApp)
	mux.HandleFunc("DELETE /api/admin/workspaces/{name}/apps/{app}", s.deleteApp)
}

type confirmBody struct {
	Confirm string `json:"confirm"`
}

func (s *Server) dashboard(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	rows, err := s.rows(ctx)
	if err != nil {
		writeErr(w, err)
		return
	}
	crit, err := s.opts.Store.ListAudit(ctx, store.AuditFilter{Severity: "critical", Limit: 10})
	if err != nil {
		writeErr(w, err)
		return
	}
	orphans, err := s.opts.Svc.Orphans(ctx)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"workspaces": rows, "critical": toAuditDTOs(crit), "orphans": len(orphans)})
}

func (s *Server) listWorkspaces(w http.ResponseWriter, r *http.Request) {
	rows, err := s.rows(r.Context())
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, rows)
}

func (s *Server) createWorkspace(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Name  string               `json:"name"`
		Quota store.Quota          `json:"quota"`
		Ports service.PortSettings `json:"ports"`
	}
	if err := decodeJSON(w, r, &in); err != nil {
		badRequest(w, err)
		return
	}
	ws, err := s.opts.Svc.CreateWorkspace(r.Context(), actor(r), in.Name, in.Quota, in.Ports)
	if err != nil {
		writeErr(w, err)
		return
	}
	row, err := s.row(r.Context(), ws)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, row)
}

func (s *Server) loadWorkspace(w http.ResponseWriter, r *http.Request) (store.Workspace, bool) {
	ws, err := s.opts.Store.GetWorkspaceByName(r.Context(), r.PathValue("name"))
	if err != nil {
		notFound(w, "workspace")
		return store.Workspace{}, false
	}
	return ws, true
}

func (s *Server) detail(r *http.Request, ws store.Workspace) (workspaceDetail, error) {
	ctx := r.Context()
	row, err := s.row(ctx, ws)
	if err != nil {
		return workspaceDetail{}, err
	}
	all, err := s.opts.Store.ListTemplates(ctx)
	if err != nil {
		return workspaceDetail{}, err
	}
	granted, err := s.opts.Store.GrantedTemplates(ctx, ws.ID)
	if err != nil {
		return workspaceDetail{}, err
	}
	tokens, err := s.opts.Store.ListTokens(ctx, ws.ID)
	if err != nil {
		return workspaceDetail{}, err
	}
	apps, err := s.opts.Svc.AdminApps(ctx, ws)
	if err != nil {
		return workspaceDetail{}, err
	}
	d := workspaceDetail{workspaceRow: row, Folder: filepath.Join(s.opts.Svc.Root, ws.Name),
		Templates: []grantDTO{}, Tokens: []tokenDTO{}, Apps: apps}
	isGranted := map[int64]bool{}
	for _, g := range granted {
		isGranted[g.ID] = true
	}
	for _, t := range all {
		d.Templates = append(d.Templates, grantDTO{ID: t.ID, Name: t.Name, HostPattern: t.HostPattern, Granted: isGranted[t.ID]})
	}
	for _, t := range tokens {
		d.Tokens = append(d.Tokens, tokenDTO{ID: t.ID, Label: t.Label, Prefix: t.Prefix, CreatedAt: t.CreatedAt, LastUsedAt: t.LastUsedAt, RevokedAt: t.RevokedAt})
	}
	return d, nil
}

func (s *Server) writeDetail(w http.ResponseWriter, r *http.Request, ws store.Workspace, status int) {
	d, err := s.detail(r, ws)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, status, d)
}

func (s *Server) getWorkspace(w http.ResponseWriter, r *http.Request) {
	if ws, ok := s.loadWorkspace(w, r); ok {
		s.writeDetail(w, r, ws, http.StatusOK)
	}
}

func (s *Server) updateWorkspace(w http.ResponseWriter, r *http.Request) {
	ws, ok := s.loadWorkspace(w, r)
	if !ok {
		return
	}
	var in struct {
		Quota store.Quota          `json:"quota"`
		Ports service.PortSettings `json:"ports"`
	}
	if err := decodeJSON(w, r, &in); err != nil {
		badRequest(w, err)
		return
	}
	ws.Quota, ws.PortRangeStart, ws.PortRangeEnd, ws.PortBindIP = in.Quota, in.Ports.Start, in.Ports.End, in.Ports.BindIP
	if err := s.opts.Svc.UpdateWorkspace(r.Context(), actor(r), ws); err != nil {
		writeErr(w, err)
		return
	}
	s.writeDetail(w, r, ws, http.StatusOK)
}

func (s *Server) deleteWorkspace(w http.ResponseWriter, r *http.Request) {
	ws, ok := s.loadWorkspace(w, r)
	if !ok {
		return
	}
	var in confirmBody
	if err := decodeJSON(w, r, &in); err != nil {
		badRequest(w, err)
		return
	}
	if in.Confirm != ws.Name {
		writeJSON(w, http.StatusUnprocessableEntity, apiError{Code: service.CodeValidation, Message: "confirm must equal the workspace name"})
		return
	}
	if err := s.opts.Svc.DeleteWorkspace(r.Context(), actor(r), ws); err != nil {
		writeErr(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) setGrants(w http.ResponseWriter, r *http.Request) {
	ws, ok := s.loadWorkspace(w, r)
	if !ok {
		return
	}
	var in struct {
		TemplateIDs []int64 `json:"template_ids"`
	}
	if err := decodeJSON(w, r, &in); err != nil {
		badRequest(w, err)
		return
	}
	if err := s.opts.Svc.SetGrants(r.Context(), actor(r), ws, in.TemplateIDs); err != nil {
		writeErr(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) createToken(w http.ResponseWriter, r *http.Request) {
	ws, ok := s.loadWorkspace(w, r)
	if !ok {
		return
	}
	var in struct {
		Label string `json:"label"`
	}
	if err := decodeJSON(w, r, &in); err != nil {
		badRequest(w, err)
		return
	}
	tok, err := s.opts.Svc.CreateToken(r.Context(), actor(r), ws, in.Label)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, map[string]string{"token": tok})
}

func (s *Server) revokeToken(w http.ResponseWriter, r *http.Request) {
	ws, ok := s.loadWorkspace(w, r)
	if !ok {
		return
	}
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		notFound(w, "token")
		return
	}
	if err := s.opts.Svc.RevokeToken(r.Context(), actor(r), ws, id); err != nil {
		writeErr(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) stopApp(w http.ResponseWriter, r *http.Request) {
	ws, ok := s.loadWorkspace(w, r)
	if !ok {
		return
	}
	if err := s.opts.Svc.AdminStopApp(r.Context(), actor(r), ws, r.PathValue("app")); err != nil {
		writeErr(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) deleteApp(w http.ResponseWriter, r *http.Request) {
	ws, ok := s.loadWorkspace(w, r)
	if !ok {
		return
	}
	var in confirmBody
	if err := decodeJSON(w, r, &in); err != nil {
		badRequest(w, err)
		return
	}
	app := r.PathValue("app")
	if in.Confirm != app {
		writeJSON(w, http.StatusUnprocessableEntity, apiError{Code: service.CodeValidation, Message: "confirm must equal the app name"})
		return
	}
	if err := s.opts.Svc.AdminDeleteApp(r.Context(), actor(r), ws, app); err != nil {
		writeErr(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
