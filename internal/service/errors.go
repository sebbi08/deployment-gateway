package service

import (
	"encoding/json"

	"github.com/sebbi08/deployment-gateway/internal/stack"
)

const (
	CodeUnauthorized = "unauthorized"
	CodeValidation   = "validation_failed"
	CodeQuota        = "quota_exceeded"
	CodePortTaken    = "port_taken"
	CodeNotFound     = "not_found"
	CodeConflict     = "conflict"
	CodeDeployFailed = "deploy_failed"
	CodeSecurity     = "security_violation"
	CodeInternal     = "internal"
)

// Error is the structured error returned to agents. detail is recorded in
// the audit log and shown to admins, never to agents.
type Error struct {
	Code    string       `json:"code"`
	Message string       `json:"message"`
	Fields  stack.Errors `json:"errors,omitempty"`

	detail   string
	critical bool
}

func (e *Error) Error() string {
	b, _ := json.Marshal(e)
	return string(b)
}

func (e *Error) Detail() string { return e.detail }

const appRule = "must match ^[a-z][a-z0-9-]{0,22}[a-z0-9]$"

func validationError(field, msg string) *Error {
	return &Error{Code: CodeValidation, Message: msg, Fields: stack.Errors{{Field: field, Message: msg}}}
}

func notFoundError(what string) *Error {
	return &Error{Code: CodeNotFound, Message: what + " not found"}
}
