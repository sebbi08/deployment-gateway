package admin

import (
	"net/http"
	"strconv"

	"github.com/sebbi08/deployment-gateway/internal/names"
	"github.com/sebbi08/deployment-gateway/internal/store"
)

func (s *Server) miscRoutes(mux *http.ServeMux) {
	s.templateRoutes(mux)
	mux.HandleFunc("GET /api/admin/audit", s.audit)
	mux.HandleFunc("GET /api/admin/settings", s.getSettings)
	mux.HandleFunc("PUT /api/admin/settings", s.putSettings)
	mux.HandleFunc("GET /api/admin/orphans", s.listOrphans)
	mux.HandleFunc("DELETE /api/admin/orphans/{id}", s.removeOrphan)
}

func (s *Server) audit(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	f := store.AuditFilter{Workspace: q.Get("workspace"), Action: q.Get("action"), Result: q.Get("result"), Severity: q.Get("severity"), Limit: 100}
	f.BeforeID, _ = strconv.ParseInt(q.Get("before"), 10, 64)
	if l, err := strconv.Atoi(q.Get("limit")); err == nil && l > 0 && l <= 500 {
		f.Limit = l
	}
	entries, err := s.opts.Store.ListAudit(r.Context(), f)
	if err != nil {
		writeErr(w, err)
		return
	}
	next := int64(0)
	if len(entries) == f.Limit {
		next = entries[len(entries)-1].ID
	}
	writeJSON(w, http.StatusOK, map[string]any{"entries": toAuditDTOs(entries), "next_before": next})
}

type settingsBody struct {
	DefaultQuota       store.Quota `json:"default_quota"`
	AuditRetentionDays int         `json:"audit_retention_days"`
}

func (s *Server) currentSettings(r *http.Request) (settingsBody, error) {
	q, err := s.opts.Store.DefaultQuota(r.Context())
	if err != nil {
		return settingsBody{}, err
	}
	days, err := s.opts.Store.AuditRetentionDays(r.Context())
	return settingsBody{DefaultQuota: q, AuditRetentionDays: days}, err
}

func (s *Server) getSettings(w http.ResponseWriter, r *http.Request) {
	cur, err := s.currentSettings(r)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, cur)
}

func (s *Server) putSettings(w http.ResponseWriter, r *http.Request) {
	var in settingsBody
	if err := decodeJSON(w, r, &in); err != nil {
		badRequest(w, err)
		return
	}
	if err := s.opts.Svc.UpdateSettings(r.Context(), actor(r), in.DefaultQuota, in.AuditRetentionDays); err != nil {
		writeErr(w, err)
		return
	}
	s.getSettings(w, r)
}

type orphanDTO struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	Workspace string `json:"workspace"`
	App       string `json:"app"`
	State     string `json:"state"`
	Status    string `json:"status"`
}

func (s *Server) listOrphans(w http.ResponseWriter, r *http.Request) {
	list, err := s.opts.Svc.Orphans(r.Context())
	if err != nil {
		writeErr(w, err)
		return
	}
	out := make([]orphanDTO, 0, len(list))
	for _, c := range list {
		out = append(out, orphanDTO{ID: c.ID, Name: c.Name, Workspace: c.Labels[names.LabelWorkspace], App: c.Labels[names.LabelApp], State: c.State, Status: c.Status})
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) removeOrphan(w http.ResponseWriter, r *http.Request) {
	if err := s.opts.Svc.RemoveOrphan(r.Context(), actor(r), r.PathValue("id")); err != nil {
		writeErr(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
