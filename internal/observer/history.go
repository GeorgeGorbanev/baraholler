package observer

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sort"
	"sync"

	"github.com/gotd/td/tg"
)

const historyPageSize = 100

type historyAPI interface {
	GetHistory(context.Context, tg.InputPeerClass, int, int, int) ([]tg.MessageClass, error)
}

type telegramHistoryAPI struct {
	client *tg.Client
}

func (a telegramHistoryAPI) GetHistory(
	ctx context.Context,
	peer tg.InputPeerClass,
	offsetID int,
	minID int,
	limit int,
) ([]tg.MessageClass, error) {
	result, err := a.client.MessagesGetHistory(ctx, &tg.MessagesGetHistoryRequest{
		Peer:     peer,
		OffsetID: offsetID,
		Limit:    limit,
		MinID:    minID,
	})
	if err != nil {
		return nil, fmt.Errorf("telegram message history fetch failed: %w", err)
	}
	modified, ok := result.AsModified()
	if !ok {
		return nil, errors.New("telegram message history invalid: unmodified response missing request hash")
	}
	return modified.GetMessages(), nil
}

type historyRecoverer struct {
	history   historyAPI
	processor *processor
	store     deliveryStore
	logger    *slog.Logger
	sources   map[int64]source
	requests  chan historyRecoveryRequest

	mu      sync.Mutex
	pending map[int64]struct{}
}

type historyRecoveryRequest struct {
	sourceID       int64
	afterMessageID int
}

func newHistoryRecoverer(
	history historyAPI,
	routes map[int64][]route,
	processor *processor,
	store deliveryStore,
	logger *slog.Logger,
) *historyRecoverer {
	sources := make(map[int64]source, len(routes))
	for sourceID, sourceRoutes := range routes {
		if len(sourceRoutes) > 0 {
			sources[sourceID] = sourceRoutes[0].source
		}
	}
	return &historyRecoverer{
		history:   history,
		processor: processor,
		store:     store,
		logger:    logger,
		sources:   sources,
		requests:  make(chan historyRecoveryRequest, len(sources)),
		pending:   make(map[int64]struct{}),
	}
}

func (r *historyRecoverer) Initialize(ctx context.Context) error {
	for sourceID, source := range r.sources {
		_, found, err := r.store.SourceMessageID(ctx, sourceID)
		if err != nil {
			return fmt.Errorf("source message position load failed: source_id=%d: %w", sourceID, err)
		}
		if found {
			continue
		}
		messages, err := r.history.GetHistory(ctx, source.peer, 0, 0, 1)
		if err != nil {
			return fmt.Errorf("source message position initialization failed: source_id=%d: %w", sourceID, err)
		}
		latestID := 0
		for _, message := range messages {
			if message.GetID() > latestID {
				latestID = message.GetID()
			}
		}
		if err := r.store.AdvanceSourceMessageID(ctx, sourceID, latestID); err != nil {
			return fmt.Errorf("source message position save failed: source_id=%d: %w", sourceID, err)
		}
		r.logger.Debug("source message position initialized",
			"source_id", sourceID,
			"message_id", latestID,
		)
	}
	return nil
}

func (r *historyRecoverer) Enqueue(sourceID int64) (bool, error) {
	if _, ok := r.sources[sourceID]; !ok {
		return false, nil
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, ok := r.pending[sourceID]; ok {
		return true, nil
	}
	afterMessageID, found, err := r.store.SourceMessageID(context.Background(), sourceID)
	if err != nil {
		return true, fmt.Errorf("source message position load failed: %w", err)
	}
	if !found {
		return true, errors.New("source message position missing")
	}
	r.pending[sourceID] = struct{}{}
	r.requests <- historyRecoveryRequest{sourceID: sourceID, afterMessageID: afterMessageID}
	return true, nil
}

func (r *historyRecoverer) Run(ctx context.Context) error {
	for {
		select {
		case <-ctx.Done():
			return nil
		case request := <-r.requests:
			if err := r.recover(ctx, request); err != nil && ctx.Err() == nil {
				r.logger.Error("history recovery failed",
					"source_id", request.sourceID,
					"error", err,
				)
			}
			r.mu.Lock()
			delete(r.pending, request.sourceID)
			r.mu.Unlock()
		}
	}
}

func (r *historyRecoverer) recover(ctx context.Context, request historyRecoveryRequest) error {
	sourceID := request.sourceID
	source, ok := r.sources[sourceID]
	if !ok {
		return fmt.Errorf("source unconfigured: source_id=%d", sourceID)
	}
	r.logger.Info("history recovery started",
		"source_id", sourceID,
		"after_message_id", request.afterMessageID,
	)
	messages, err := r.loadAfter(ctx, source, request.afterMessageID)
	if err != nil {
		return err
	}
	for _, message := range messages {
		r.processor.Handle(ctx, message)
	}
	r.logger.Info("history recovery completed",
		"source_id", sourceID,
		"recovered_messages", len(messages),
	)
	return nil
}

func (r *historyRecoverer) loadAfter(ctx context.Context, source source, minMessageID int) ([]*tg.Message, error) {
	var (
		messages []*tg.Message
		offsetID int
	)
	seen := make(map[int]struct{})
	for {
		page, err := r.history.GetHistory(ctx, source.peer, offsetID, minMessageID, historyPageSize)
		if err != nil {
			return nil, err
		}
		if len(page) == 0 {
			break
		}

		oldestID := 0
		reachedPosition := false
		for _, messageClass := range page {
			messageID := messageClass.GetID()
			if oldestID == 0 || messageID < oldestID {
				oldestID = messageID
			}
			if messageID <= minMessageID {
				reachedPosition = true
				continue
			}
			message, ok := messageClass.(*tg.Message)
			if !ok {
				continue
			}
			if _, ok := seen[messageID]; ok {
				continue
			}
			seen[messageID] = struct{}{}
			messages = append(messages, message)
		}
		if reachedPosition || len(page) < historyPageSize {
			break
		}
		if oldestID <= 0 || (offsetID > 0 && oldestID >= offsetID) {
			return nil, errors.New("telegram history pagination stalled")
		}
		offsetID = oldestID
	}

	sort.Slice(messages, func(i, j int) bool {
		return messages[i].GetID() < messages[j].GetID()
	})
	return messages, nil
}
