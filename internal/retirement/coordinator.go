// Package retirement converges durable FeatureArea stop intents using the
// canonical owner cancellation protocol. It never owns backend signals.
package retirement

import (
	"context"
	"sync"
	"time"

	"github.com/lazyun/devbridge/internal/executioncancel"
)

type Store interface {
	PendingRetirements(context.Context) ([]string, error)
	RetirementTargets(context.Context, string) ([]executioncancel.Input, error)
	FinishRetirement(context.Context, string, string) error
}
type Canceler interface {
	CancelExecution(context.Context, executioncancel.Input) (executioncancel.Receipt, error)
}
type Coordinator struct {
	store         Store
	canceler      Canceler
	cancel        context.CancelFunc
	wake          chan struct{}
	done          chan struct{}
	once          sync.Once
	continuations []func(context.Context)
}

func Start(store Store, canceler Canceler, continuations ...func(context.Context)) *Coordinator {
	ctx, cancel := context.WithCancel(context.Background())
	c := &Coordinator{continuations: continuations, store: store, canceler: canceler, cancel: cancel, wake: make(chan struct{}, 1), done: make(chan struct{})}
	go c.loop(ctx)
	c.Wake()
	return c
}
func (c *Coordinator) Wake() {
	select {
	case c.wake <- struct{}{}:
	default:
	}
}
func (c *Coordinator) Close() {
	if c == nil {
		return
	}
	c.once.Do(func() { c.cancel(); <-c.done })
}
func (c *Coordinator) loop(ctx context.Context) {
	defer close(c.done)
	// Internal write-side reconciliation also observes finalization/recovery and
	// claim release committed outside the owned runtime, including restart gaps.
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()
	var lastContinuation time.Time
	for {
		kicked := false
		select {
		case <-ctx.Done():
			return
		case <-c.wake:
			kicked = true
		case <-ticker.C:
		}
		ids, err := c.store.PendingRetirements(ctx)
		if err != nil {
			continue
		}
		for _, id := range ids {
			if ctx.Err() != nil {
				return
			}
			c.Advance(ctx, id)
		}
		if kicked || time.Since(lastContinuation) >= time.Second {
			lastContinuation = time.Now()
			for _, reconcile := range c.continuations {
				reconcile(ctx)
			}
		}
	}
}
func (c *Coordinator) Advance(ctx context.Context, id string) {
	targets, err := c.store.RetirementTargets(ctx, id)
	if err != nil {
		_ = c.store.FinishRetirement(ctx, id, err.Error())
		return
	}
	detail := ""
	for _, target := range targets {
		if _, err = c.canceler.CancelExecution(ctx, target); err != nil {
			detail = err.Error()
		}
	}
	_ = c.store.FinishRetirement(ctx, id, detail)
}
