// Package taskscope carries owner authority already checked by machine admission.
package taskscope

import (
	"context"
	"github.com/google/uuid"
)

type Owner struct {
	Tenant       string `json:"tenant_id"`
	Organization string `json:"organization_id"`
	Owner        string `json:"owner_id"`
}
type Binding struct {
	Owner
	Task       string
	ModelLease string
}
type key struct{}

func ValidID(s string) bool {
	id, err := uuid.Parse(s)
	return err == nil && id != uuid.Nil && id.String() == s
}
func (s Owner) Valid() bool { return ValidID(s.Tenant) && ValidID(s.Organization) && ValidID(s.Owner) }

// With binds trusted internal authority; HTTP callers cannot supply this context key.
func With(ctx context.Context, b Binding) context.Context { return context.WithValue(ctx, key{}, b) }
func From(ctx context.Context) *Binding {
	b, ok := ctx.Value(key{}).(Binding)
	if !ok {
		return nil
	}
	return &b
}
