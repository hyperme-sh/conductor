// Package permissiondream schedules bounded permission-review batches.
package permissiondream

import (
	"context"
	"errors"
	"time"
)

// Batch must resume any persisted pending review before starting a new one.
// It is responsible for owner revision checks and idempotent application.
type Batch func(context.Context) error

// Run serializes review attempts. Missed ticks coalesce; no backlog is created.
// A failed attempt waits for the next tick rather than spinning. The caller
// supplies the session context, so disconnect or consent withdrawal stops work.
func Run(ctx context.Context, interval, timeout time.Duration, batch Batch, report func(error)) error {
	if interval < time.Minute || interval > 24*time.Hour || timeout <= 0 || timeout > time.Minute || batch == nil {
		return errors.New("invalid permission review schedule")
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	return run(ctx, ticker.C, timeout, batch, report)
}

func run(ctx context.Context, ticks <-chan time.Time, timeout time.Duration, batch Batch, report func(error)) error {
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case _, ok := <-ticks:
			if !ok {
				return nil
			}
			if err := ctx.Err(); err != nil {
				return err
			}
			attempt, cancel := context.WithTimeout(ctx, timeout)
			err := batch(attempt)
			cancel()
			if err != nil && report != nil {
				report(err)
			}
		}
	}
}
