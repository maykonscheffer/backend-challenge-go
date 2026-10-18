package messaging

import (
	"context"
	"example.com/wallet/internal/store"
	"go.uber.org/fx"
	"log/slog"
	"time"
)

type PendingWorker struct{}

func NewPendingWorker(lc fx.Lifecycle, s *store.Store) *PendingWorker {
	var cancel context.CancelFunc
	var done chan struct{}
	lc.Append(fx.Hook{OnStart: func(context.Context) error {
		var ctx context.Context
		ctx, cancel = context.WithCancel(context.Background())
		done = make(chan struct{})
		go func() {
			defer close(done)
			for ctx.Err() == nil {
				attempt, stop := context.WithTimeout(ctx, 10*time.Second)
				err := s.ResumePending(attempt)
				stop()
				if err != nil && ctx.Err() == nil {
					slog.Warn("pending reference retry failed", "error", err)
				}
				if !wait(ctx, 500*time.Millisecond) {
					return
				}
			}
		}()
		return nil
	}, OnStop: func(ctx context.Context) error {
		if cancel == nil {
			return nil
		}
		cancel()
		select {
		case <-done:
			return nil
		case <-ctx.Done():
			return ctx.Err()
		}
	}})
	return &PendingWorker{}
}
