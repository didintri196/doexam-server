// Package opt provides JSON field types for partial updates (PATCH).
package opt

import (
	"bytes"
	"encoding/json"
)

// Nullable distinguishes three states of a JSON field:
// absent (Set=false), null (Set=true, Null=true) and a value (Set=true, Null=false).
type Nullable[T any] struct {
	Set   bool
	Null  bool
	Value T
}

// UnmarshalJSON is only called when the field is present.
func (n *Nullable[T]) UnmarshalJSON(b []byte) error {
	n.Set = true
	if bytes.Equal(bytes.TrimSpace(b), []byte("null")) {
		n.Null = true
		return nil
	}
	return json.Unmarshal(b, &n.Value)
}

// Ptr returns the value as a pointer (nil when null). Only meaningful when Set.
func (n Nullable[T]) Ptr() *T {
	if n.Null {
		return nil
	}
	v := n.Value
	return &v
}
