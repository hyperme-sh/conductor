package permissiondream

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestScheduleRetriesOnNextTickAndStopsWithSession(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	ticks := make(chan time.Time, 2)
	ticks <- time.Now()
	ticks <- time.Now()
	calls, reports := 0, 0
	err := run(ctx, ticks, time.Second, func(attempt context.Context) error {
		calls++
		if _, ok := attempt.Deadline(); !ok {
			t.Fatal("missing batch deadline")
		}
		if calls == 2 {
			cancel()
		}
		return errors.New("owner unavailable")
	}, func(error) { reports++ })
	if !errors.Is(err, context.Canceled) || calls != 2 || reports != 2 {
		t.Fatalf("%v %d %d", err, calls, reports)
	}
}
func TestCanceledSessionDoesNotStartReadyBatch(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	ticks := make(chan time.Time, 1)
	ticks <- time.Now()
	err := run(ctx, ticks, time.Second, func(context.Context) error { t.Fatal("ran canceled batch"); return nil }, nil)
	if !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
}
func TestRejectUnboundedSchedule(t *testing.T) {
	for _, interval := range []time.Duration{0, time.Second, 25 * time.Hour} {
		if Run(context.Background(), interval, time.Second, func(context.Context) error { return nil }, nil) == nil {
			t.Fatal("invalid interval accepted")
		}
	}
}
