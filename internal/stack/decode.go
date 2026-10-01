package stack

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
)

const MaxRequestBytes = 128 << 10

// Decode parses raw JSON into a Stack, rejecting unknown fields at every
// level, trailing data and oversize payloads.
func Decode(raw []byte) (*Stack, error) {
	if len(raw) > MaxRequestBytes {
		return nil, Errors{{Message: fmt.Sprintf("stack exceeds %d bytes", MaxRequestBytes)}}
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	var s Stack
	if err := dec.Decode(&s); err != nil {
		return nil, Errors{{Message: "invalid stack JSON: " + err.Error()}}
	}
	if err := dec.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return nil, Errors{{Message: "invalid stack JSON: trailing data after object"}}
	}
	return &s, nil
}
