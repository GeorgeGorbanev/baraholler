package observer

import (
	"context"
	"sync"

	"github.com/gotd/td/telegram"
	"github.com/gotd/td/tg"
)

type updateBuffer struct {
	mu      sync.Mutex
	active  bool
	target  telegram.UpdateHandler
	pending []tg.UpdatesClass
}

func (b *updateBuffer) Handle(ctx context.Context, update tg.UpdatesClass) error {
	b.mu.Lock()
	if !b.active {
		b.pending = append(b.pending, update)
		b.mu.Unlock()
		return nil
	}
	target := b.target
	b.mu.Unlock()
	return target.Handle(ctx, update)
}

func (b *updateBuffer) Activate(ctx context.Context, target telegram.UpdateHandler) error {
	b.mu.Lock()
	b.target = target
	b.mu.Unlock()

	for {
		b.mu.Lock()
		pending := b.pending
		b.pending = nil
		if len(pending) == 0 {
			b.active = true
			b.mu.Unlock()
			return nil
		}
		b.mu.Unlock()

		for _, update := range pending {
			if err := target.Handle(ctx, update); err != nil {
				return err
			}
		}
	}
}
