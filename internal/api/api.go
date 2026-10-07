// Package api exposes the agent tools as a small JSON HTTP API, mirroring
// the MCP tools one to one.
package api

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"

	"github.com/sebbi08/deployment-gateway/internal/agentauth"
	"github.com/sebbi08/deployment-gateway/internal/service"
	"github.com/sebbi08/deployment-gateway/internal/stack"
)

type Handler struct {
	Svc  *service.Service
	Auth *agentauth.Authenticator
}

func (h *Handler) Routes() http.Handler {
	mux := http.NewServeMux()
	s := h.Svc
	handle(mux, s, "whoami", func(ctx context.Context, p service.Principal, _ service.NoInput) (any, error) {
		return s.Whoami(ctx, p)
	})
	handle(mux, s, "list_templates", func(ctx context.Context, p service.Principal, _ service.NoInput) (any, error) {
		return s.ListTemplates(ctx, p)
	})
	handle(mux, s, "validate", func(ctx context.Context, p service.Principal, in service.DeployInput) (any, error) {
		return s.Validate(ctx, p, in)
	})
	handle(mux, s, "deploy", func(ctx context.Context, p service.Principal, in service.DeployInput) (any, error) {
		return s.Deploy(ctx, p, in)
	})
	handle(mux, s, "list", func(ctx context.Context, p service.Principal, _ service.NoInput) (any, error) { return s.List(ctx, p) })
	handle(mux, s, "logs", func(ctx context.Context, p service.Principal, in service.LogsInput) (any, error) {
		return s.Logs(ctx, p, in)
	})
	handle(mux, s, "restart", func(ctx context.Context, p service.Principal, in service.ServiceInput) (any, error) {
		return s.Restart(ctx, p, in)
	})
	handle(mux, s, "stop", func(ctx context.Context, p service.Principal, in service.ServiceInput) (any, error) {
		return s.Stop(ctx, p, in)
	})
	handle(mux, s, "delete", func(ctx context.Context, p service.Principal, in service.DeleteInput) (any, error) {
		return s.Delete(ctx, p, in)
	})
	return h.Auth.Middleware(mux)
}

func handle[In any](mux *http.ServeMux, s *service.Service, name string, fn func(context.Context, service.Principal, In) (any, error)) {
	mux.HandleFunc("POST /api/v1/"+name, func(w http.ResponseWriter, r *http.Request) {
		p, ok := agentauth.FromContext(r.Context())
		if !ok {
			writeJSON(w, http.StatusUnauthorized, &service.Error{Code: service.CodeUnauthorized, Message: "unauthorized"})
			return
		}
		var in In
		if body, err := decodeStrict(w, r, &in); err != nil {
			msg := "invalid request body: " + err.Error()
			s.AuditRejected(r.Context(), p, name, body, msg)
			writeJSON(w, http.StatusBadRequest, &service.Error{Code: service.CodeValidation, Message: msg})
			return
		}
		res, err := fn(r.Context(), p, in)
		if err != nil {
			var se *service.Error
			if !errors.As(err, &se) {
				se = &service.Error{Code: service.CodeInternal, Message: "internal error"}
			}
			writeJSON(w, StatusFor(se.Code), se)
			return
		}
		writeJSON(w, http.StatusOK, res)
	})
}

// decodeStrict rejects unknown fields at every nesting level (including the
// stack), trailing data and oversize bodies. An empty body means {}.
// It returns the raw body for auditing.
func decodeStrict(w http.ResponseWriter, r *http.Request, v any) ([]byte, error) {
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, stack.MaxRequestBytes+4096))
	if err != nil {
		return body, err
	}
	if len(bytes.TrimSpace(body)) == 0 {
		body = []byte("{}")
	}
	dec := json.NewDecoder(bytes.NewReader(body))
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		return body, err
	}
	if err := dec.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return body, errors.New("trailing data after JSON object")
	}
	return body, nil
}

// StatusFor maps a service error code to an HTTP status (also used by the admin API).
func StatusFor(code string) int {
	switch code {
	case service.CodeUnauthorized:
		return http.StatusUnauthorized
	case service.CodeValidation, service.CodeQuota, service.CodeSecurity:
		return http.StatusUnprocessableEntity
	case service.CodePortTaken, service.CodeConflict:
		return http.StatusConflict
	case service.CodeNotFound:
		return http.StatusNotFound
	case service.CodeDeployFailed:
		return http.StatusBadGateway
	default:
		return http.StatusInternalServerError
	}
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	json.NewEncoder(w).Encode(v)
}
