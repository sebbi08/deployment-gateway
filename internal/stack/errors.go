package stack

import (
	"fmt"
	"strings"
)

type FieldError struct {
	Field   string `json:"field"`
	Message string `json:"message"`
}

// Errors collects every validation problem, not just the first.
type Errors []FieldError

func (e Errors) Error() string {
	parts := make([]string, len(e))
	for i, fe := range e {
		if fe.Field == "" {
			parts[i] = fe.Message
		} else {
			parts[i] = fe.Field + ": " + fe.Message
		}
	}
	return strings.Join(parts, "; ")
}

func (e *Errors) add(field, format string, args ...any) {
	*e = append(*e, FieldError{Field: field, Message: fmt.Sprintf(format, args...)})
}
