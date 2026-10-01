package admin

import (
	"errors"
	"maps"
	"net/http"
	"slices"
	"strconv"

	"github.com/sebmahr/deploy-gateway/internal/store"
	"github.com/sebmahr/deploy-gateway/internal/tmpl"
)

type templateDTO struct {
	ID          int64             `json:"id"`
	Name        string            `json:"name"`
	Description string            `json:"description"`
	HostPattern string            `json:"host_pattern"`
	Networks    []string          `json:"networks"`
	Labels      map[string]string `json:"labels"`
}

func toTemplateDTO(t store.StoredTemplate) templateDTO {
	d := templateDTO{ID: t.ID, Name: t.Name, Description: t.Description, HostPattern: t.HostPattern, Networks: t.Networks, Labels: t.Labels}
	if d.Networks == nil {
		d.Networks = []string{}
	}
	if d.Labels == nil {
		d.Labels = map[string]string{}
	}
	return d
}

type labelRow struct {
	Key   string `json:"key"`
	Value string `json:"value"`
}

type previewResponse struct {
	Valid  bool       `json:"valid"`
	Errors []string   `json:"errors"`
	Host   string     `json:"host"`
	Labels []labelRow `json:"labels"`
}

func (s *Server) templateRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/admin/templates", s.listTemplates)
	mux.HandleFunc("POST /api/admin/templates", s.createTemplate)
	mux.HandleFunc("POST /api/admin/templates/preview", s.previewTemplate)
	mux.HandleFunc("GET /api/admin/templates/{id}", s.getTemplate)
	mux.HandleFunc("PUT /api/admin/templates/{id}", s.updateTemplate)
	mux.HandleFunc("DELETE /api/admin/templates/{id}", s.deleteTemplate)
}

func templateID(w http.ResponseWriter, r *http.Request) (int64, bool) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil || id <= 0 {
		notFound(w, "template")
		return 0, false
	}
	return id, true
}

func (s *Server) listTemplates(w http.ResponseWriter, r *http.Request) {
	all, err := s.opts.Store.ListTemplates(r.Context())
	if err != nil {
		writeErr(w, err)
		return
	}
	out := make([]templateDTO, 0, len(all))
	for _, t := range all {
		out = append(out, toTemplateDTO(t))
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) getTemplate(w http.ResponseWriter, r *http.Request) {
	id, ok := templateID(w, r)
	if !ok {
		return
	}
	t, err := s.opts.Store.GetTemplate(r.Context(), id)
	if errors.Is(err, store.ErrNotFound) {
		notFound(w, "template")
		return
	}
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, toTemplateDTO(t))
}

func (s *Server) createTemplate(w http.ResponseWriter, r *http.Request) {
	var t tmpl.Template
	if err := decodeJSON(w, r, &t); err != nil {
		badRequest(w, err)
		return
	}
	id, err := s.opts.Svc.SaveTemplate(r.Context(), actor(r), 0, t)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, map[string]int64{"id": id})
}

func (s *Server) updateTemplate(w http.ResponseWriter, r *http.Request) {
	id, ok := templateID(w, r)
	if !ok {
		return
	}
	var t tmpl.Template
	if err := decodeJSON(w, r, &t); err != nil {
		badRequest(w, err)
		return
	}
	if _, err := s.opts.Svc.SaveTemplate(r.Context(), actor(r), id, t); err != nil {
		writeErr(w, err)
		return
	}
	stored, err := s.opts.Store.GetTemplate(r.Context(), id)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, toTemplateDTO(stored))
}

func (s *Server) deleteTemplate(w http.ResponseWriter, r *http.Request) {
	id, ok := templateID(w, r)
	if !ok {
		return
	}
	if err := s.opts.Svc.DeleteTemplate(r.Context(), actor(r), id); err != nil {
		writeErr(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// previewTemplate validates a draft (including hostname uniqueness against
// other stored templates) and renders it with sample values. It never saves
// and does not check Docker networks; that happens on save.
func (s *Server) previewTemplate(w http.ResponseWriter, r *http.Request) {
	var in struct {
		ID       int64         `json:"id"`
		Template tmpl.Template `json:"template"`
	}
	if err := decodeJSON(w, r, &in); err != nil {
		badRequest(w, err)
		return
	}
	all, err := s.opts.Store.ListTemplates(r.Context())
	if err != nil {
		writeErr(w, err)
		return
	}
	var others []tmpl.Template
	for _, o := range all {
		if o.ID != in.ID {
			others = append(others, o.Template)
		}
	}
	resp := previewResponse{Valid: true, Errors: []string{}, Labels: []labelRow{}}
	var verr tmpl.ValidationError
	if err := tmpl.Validate(in.Template, others); errors.As(err, &verr) {
		resp.Valid, resp.Errors = false, verr
	}
	vars := tmpl.Vars{Workspace: "alice", App: "shop", Service: "web", Port: 8080}
	resp.Host = in.Template.Host(vars)
	rendered := in.Template.RenderLabels(vars)
	for _, k := range slices.Sorted(maps.Keys(rendered)) {
		resp.Labels = append(resp.Labels, labelRow{Key: k, Value: rendered[k]})
	}
	writeJSON(w, http.StatusOK, resp)
}
