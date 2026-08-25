package observer

import (
	"context"
	"log/slog"
	"sync"
	"time"

	"github.com/GeorgeGorbanev/baraholler/internal/matcher"
	"github.com/GeorgeGorbanev/baraholler/internal/notifier"
	"github.com/GeorgeGorbanev/baraholler/internal/state"
	"github.com/gotd/td/tg"
)

const (
	deliveryRetryInitialDelay = 5 * time.Second
	deliveryRetryMaxDelay     = 15 * time.Minute
	deliveryRetryScanInterval = time.Second
)

type deliveryStore interface {
	Delivered(context.Context, state.DeliveryKey) (bool, error)
	MarkDelivered(context.Context, state.DeliveryKey) error
	PendingDelivery(context.Context, state.DeliveryKey) (state.PendingDelivery, bool, error)
	PendingDeliveries(context.Context) ([]state.PendingDelivery, error)
	SavePendingDelivery(context.Context, state.PendingDelivery) error
	DeletePendingDelivery(context.Context, state.DeliveryKey) error
	SourceMessageID(context.Context, int64) (int, bool, error)
	AdvanceSourceMessageID(context.Context, int64, int) error
}

type source struct {
	id         int64
	name       string
	url        string
	peer       tg.InputPeerClass
	noForwards bool
}

type route struct {
	ruleName  string
	matcher   matcher.Matcher
	source    source
	notifiers []notifier.Notifier
}

type processor struct {
	mu       sync.Mutex
	inflight map[state.DeliveryKey]struct{}
	missing  map[state.DeliveryKey]struct{}
	routes   map[int64][]route
	store    deliveryStore
	logger   *slog.Logger
	now      func() time.Time
	retry    chan struct{}
}

func newProcessor(routes map[int64][]route, store deliveryStore, logger *slog.Logger) *processor {
	return &processor{
		inflight: make(map[state.DeliveryKey]struct{}),
		missing:  make(map[state.DeliveryKey]struct{}),
		routes:   routes,
		store:    store,
		logger:   logger,
		now:      time.Now,
		retry:    make(chan struct{}, 1),
	}
}

func (p *processor) RunRetries(ctx context.Context) error {
	p.retryPending(ctx)
	ticker := time.NewTicker(deliveryRetryScanInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
			p.retryPending(ctx)
		case <-p.retry:
			p.retryPending(ctx)
		}
	}
}

func (p *processor) Handle(ctx context.Context, message *tg.Message) {
	sourceID, ok := messageSourceID(message.GetPeerID())
	if !ok {
		return
	}
	routes := p.routes[sourceID]
	if len(routes) == 0 {
		return
	}
	defer func() {
		if err := p.store.AdvanceSourceMessageID(ctx, sourceID, message.GetID()); err != nil {
			p.logger.Error("source message position update failed",
				"source_id", sourceID,
				"message_id", message.GetID(),
				"error", err,
			)
		}
	}()

	for _, route := range routes {
		matchedTerms := route.matcher.Match(message.GetMessage())
		if len(matchedTerms) == 0 {
			continue
		}
		match := notifier.Match{
			RuleName:     route.ruleName,
			SourceName:   route.source.name,
			SourceURL:    route.source.url,
			SourceID:     route.source.id,
			SourcePeer:   route.source.peer,
			MessageID:    message.GetID(),
			Text:         message.GetMessage(),
			MatchedTerms: matchedTerms,
			NoForwards:   message.GetNoforwards() || route.source.noForwards,
		}

		for _, destination := range route.notifiers {
			key := state.DeliveryKey{
				Rule:       route.ruleName,
				SourceID:   sourceID,
				MessageID:  message.GetID(),
				NotifierID: destination.ID(),
			}
			if !p.beginDelivery(key) {
				continue
			}
			p.deliver(ctx, route, match, destination, key, "")
			p.endDelivery(key)
		}
	}
}

func (p *processor) deliver(
	ctx context.Context,
	route route,
	match notifier.Match,
	destination notifier.Notifier,
	key state.DeliveryKey,
	checkpoint string,
) {
	sourceID := match.SourceID
	delivered, err := p.store.Delivered(ctx, key)
	if err != nil {
		p.logger.Error("delivery state read failed",
			"rule", route.ruleName,
			"source_id", sourceID,
			"message_id", match.MessageID,
			"notifier", destination.ID(),
			"error", err,
		)
		return
	}
	if delivered {
		p.clearPending(ctx, key)
		return
	}

	checkpoint, notifyErr := notifyFrom(ctx, destination, match, checkpoint)
	if notifyErr != nil {
		pending, retryErr := p.scheduleRetry(ctx, key, match, checkpoint)
		p.logger.Error("notification failed",
			"rule", route.ruleName,
			"source_id", sourceID,
			"message_id", match.MessageID,
			"notifier", destination.ID(),
			"error", notifyErr,
		)
		if retryErr != nil {
			p.logger.Error("notification retry save failed",
				"rule", route.ruleName,
				"source_id", sourceID,
				"message_id", match.MessageID,
				"notifier", destination.ID(),
				"error", retryErr,
			)
		} else {
			p.logger.Warn("notification retry scheduled",
				"rule", route.ruleName,
				"source_id", sourceID,
				"message_id", match.MessageID,
				"notifier", destination.ID(),
				"attempts", pending.Attempts,
				"retry_at", pending.NextAttemptTime,
			)
		}
		return
	}
	if err := p.store.MarkDelivered(ctx, key); err != nil {
		_, retryErr := p.scheduleRetry(ctx, key, match, checkpoint)
		p.logger.Error("delivery state save failed",
			"rule", route.ruleName,
			"source_id", sourceID,
			"message_id", match.MessageID,
			"notifier", destination.ID(),
			"error", err,
		)
		if retryErr != nil {
			p.logger.Error("notification retry save failed",
				"rule", route.ruleName,
				"source_id", sourceID,
				"message_id", match.MessageID,
				"notifier", destination.ID(),
				"error", retryErr,
			)
		}
		return
	}
	p.clearPending(ctx, key)
	p.logger.Info("notification delivered",
		"rule", route.ruleName,
		"source_id", sourceID,
		"message_id", match.MessageID,
		"notifier", destination.ID(),
	)
}

func (p *processor) retryPending(ctx context.Context) {
	pendingDeliveries, err := p.store.PendingDeliveries(ctx)
	if err != nil {
		if ctx.Err() == nil {
			p.logger.Error("notification retries load failed", "error", err)
		}
		return
	}
	now := p.now()
	for _, pending := range pendingDeliveries {
		if pending.NextAttemptTime.After(now) {
			continue
		}
		route, destination, ok := p.deliveryTarget(pending.Key)
		if !ok {
			p.logMissingRoute(pending.Key)
			continue
		}
		if !p.beginDelivery(pending.Key) {
			continue
		}
		match := notifier.Match{
			RuleName:     route.ruleName,
			SourceName:   route.source.name,
			SourceURL:    route.source.url,
			SourceID:     route.source.id,
			SourcePeer:   route.source.peer,
			MessageID:    pending.Key.MessageID,
			Text:         pending.MessageExcerpt,
			MatchedTerms: pending.MatchedTerms,
			NoForwards:   pending.NoForwards || route.source.noForwards,
		}
		p.deliver(ctx, route, match, destination, pending.Key, pending.NotifierCheckpoint)
		p.endDelivery(pending.Key)
	}
}

func (p *processor) logMissingRoute(key state.DeliveryKey) {
	p.mu.Lock()
	if _, logged := p.missing[key]; logged {
		p.mu.Unlock()
		return
	}
	p.missing[key] = struct{}{}
	p.mu.Unlock()
	p.logger.Warn("notification route missing",
		"rule", key.Rule,
		"source_id", key.SourceID,
		"message_id", key.MessageID,
		"notifier", key.NotifierID,
	)
}

func (p *processor) deliveryTarget(key state.DeliveryKey) (route, notifier.Notifier, bool) {
	for _, candidate := range p.routes[key.SourceID] {
		if candidate.ruleName != key.Rule {
			continue
		}
		for _, destination := range candidate.notifiers {
			if destination.ID() == key.NotifierID {
				return candidate, destination, true
			}
		}
	}
	return route{}, nil, false
}

func (p *processor) scheduleRetry(
	ctx context.Context,
	key state.DeliveryKey,
	match notifier.Match,
	checkpoint string,
) (state.PendingDelivery, error) {
	attempts := 1
	previous, found, err := p.store.PendingDelivery(ctx, key)
	if err != nil {
		return state.PendingDelivery{}, err
	}
	if found && previous.Attempts >= attempts {
		attempts = previous.Attempts + 1
	}
	pending := state.PendingDelivery{
		Key:                key,
		MessageExcerpt:     notifier.Snippet(match.Text),
		MatchedTerms:       append([]string(nil), match.MatchedTerms...),
		NoForwards:         match.NoForwards,
		NotifierCheckpoint: checkpoint,
		Attempts:           attempts,
		NextAttemptTime:    p.now().Add(deliveryRetryDelay(attempts)),
	}
	if err := p.store.SavePendingDelivery(ctx, pending); err != nil {
		return state.PendingDelivery{}, err
	}
	select {
	case p.retry <- struct{}{}:
	default:
	}
	return pending, nil
}

func notifyFrom(
	ctx context.Context,
	destination notifier.Notifier,
	match notifier.Match,
	checkpoint string,
) (string, error) {
	resumable, ok := destination.(notifier.ResumableNotifier)
	if !ok {
		return checkpoint, destination.Notify(ctx, match)
	}
	return resumable.NotifyFrom(ctx, match, checkpoint)
}

func (p *processor) clearPending(ctx context.Context, key state.DeliveryKey) {
	if err := p.store.DeletePendingDelivery(ctx, key); err != nil && ctx.Err() == nil {
		p.logger.Error("notification retry removal failed",
			"rule", key.Rule,
			"source_id", key.SourceID,
			"message_id", key.MessageID,
			"notifier", key.NotifierID,
			"error", err,
		)
	}
}

func deliveryRetryDelay(attempts int) time.Duration {
	delay := deliveryRetryInitialDelay
	for attempt := 1; attempt < attempts; attempt++ {
		if delay >= deliveryRetryMaxDelay/2 {
			return deliveryRetryMaxDelay
		}
		delay *= 2
	}
	if delay > deliveryRetryMaxDelay {
		return deliveryRetryMaxDelay
	}
	return delay
}

func (p *processor) beginDelivery(key state.DeliveryKey) bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	if _, exists := p.inflight[key]; exists {
		return false
	}
	p.inflight[key] = struct{}{}
	return true
}

func (p *processor) endDelivery(key state.DeliveryKey) {
	p.mu.Lock()
	delete(p.inflight, key)
	p.mu.Unlock()
}

func messageSourceID(peer tg.PeerClass) (int64, bool) {
	switch value := peer.(type) {
	case *tg.PeerChannel:
		return value.ChannelID, true
	case *tg.PeerChat:
		return value.ChatID, true
	default:
		return 0, false
	}
}
