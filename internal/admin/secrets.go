package admin

import "net/http"

func (s *Server) secretRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/admin/workspaces/{name}/secrets", s.listSecrets)
	mux.HandleFunc("POST /api/admin/workspaces/{name}/secrets", s.createSecret)
	mux.HandleFunc("PUT /api/admin/workspaces/{name}/secrets/{secret}", s.setSecret)
	mux.HandleFunc("POST /api/admin/workspaces/{name}/secrets/{secret}/apply", s.applySecret)
	mux.HandleFunc("DELETE /api/admin/workspaces/{name}/secrets/{secret}", s.deleteSecret)
}

func (s *Server) listSecrets(w http.ResponseWriter, r *http.Request) {
	ws, ok := s.loadWorkspace(w, r)
	if !ok {
		return
	}
	v, err := s.opts.Svc.AdminSecrets(r.Context(), ws)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, v)
}

func (s *Server) createSecret(w http.ResponseWriter, r *http.Request) {
	ws, ok := s.loadWorkspace(w, r)
	if !ok {
		return
	}
	var in struct {
		Name  string `json:"name"`
		Value string `json:"value"`
	}
	if err := decodeJSON(w, r, &in); err != nil {
		badRequest(w, err)
		return
	}
	if err := s.opts.Svc.CreateSecret(r.Context(), actor(r), ws, in.Name, in.Value); err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, map[string]string{"name": in.Name})
}

func (s *Server) setSecret(w http.ResponseWriter, r *http.Request) {
	ws, ok := s.loadWorkspace(w, r)
	if !ok {
		return
	}
	var in struct {
		Value string `json:"value"`
	}
	if err := decodeJSON(w, r, &in); err != nil {
		badRequest(w, err)
		return
	}
	apps, err := s.opts.Svc.SetSecret(r.Context(), actor(r), ws, r.PathValue("secret"), in.Value)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string][]string{"apps": apps})
}

func (s *Server) applySecret(w http.ResponseWriter, r *http.Request) {
	ws, ok := s.loadWorkspace(w, r)
	if !ok {
		return
	}
	apps, err := s.opts.Svc.ReapplySecret(r.Context(), actor(r), ws, r.PathValue("secret"))
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string][]string{"apps": apps})
}

func (s *Server) deleteSecret(w http.ResponseWriter, r *http.Request) {
	ws, ok := s.loadWorkspace(w, r)
	if !ok {
		return
	}
	if err := s.opts.Svc.DeleteSecret(r.Context(), actor(r), ws, r.PathValue("secret")); err != nil {
		writeErr(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
