package profileselect

import (
	"context"
	"errors"
	"sync/atomic"
)

// Admission separates a box accepting connections from its configuration commit.
// A pending candidate must not use the previous generation's live registry.
type Admission struct{ committed atomic.Bool }
type admissionContextKey struct{}

var ErrConfigurationPending = errors.New("gateway configuration is not committed")

func WithAdmission(ctx context.Context, gate *Admission) context.Context {
	return context.WithValue(ctx, admissionContextKey{}, gate)
}
func AdmissionFrom(ctx context.Context) *Admission {
	gate, _ := ctx.Value(admissionContextKey{}).(*Admission)
	return gate
}
func (g *Admission) Commit() {
	if g != nil {
		g.committed.Store(true)
	}
}
func (g *Admission) Close() {
	if g != nil {
		g.committed.Store(false)
	}
}
func (g *Admission) Check() error {
	if g != nil && !g.committed.Load() {
		return ErrConfigurationPending
	}
	return nil
}
