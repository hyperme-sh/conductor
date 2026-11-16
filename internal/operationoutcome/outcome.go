// Package operationoutcome reports whether a dispatch finished conclusively.
package operationoutcome

import (
	"context"
	"sync/atomic"
)

type key struct{}
type Result struct{ known atomic.Bool }

func Track(ctx context.Context) (context.Context, *Result) {
	r := &Result{}
	return context.WithValue(ctx, key{}, r), r
}
func Record(ctx context.Context, known bool) {
	if r, ok := ctx.Value(key{}).(*Result); ok {
		r.known.Store(known)
	}
}
func (r *Result) Known() bool { return r.known.Load() }
