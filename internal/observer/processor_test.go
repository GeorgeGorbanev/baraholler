package observer

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/GeorgeGorbanev/baraholler/internal/matcher"
	"github.com/GeorgeGorbanev/baraholler/internal/notifier"
	"github.com/GeorgeGorbanev/baraholler/internal/state"
	"github.com/gotd/td/tg"
	"github.com/stretchr/testify/require"
)

type memoryDeliveryStore struct {
	mu        sync.Mutex
	delivered map[state.DeliveryKey]bool
	pending   map[state.DeliveryKey]state.PendingDelivery
	positions map[int64]int
	markErr   error
}

func (s *memoryDeliveryStore) Delivered(_ context.Context, key state.DeliveryKey) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.delivered[key], nil
}

func (s *memoryDeliveryStore) MarkDelivered(_ context.Context, key state.DeliveryKey) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.markErr != nil {
		return s.markErr
	}
	s.delivered[key] = true
	return nil
}

func (s *memoryDeliveryStore) PendingDelivery(_ context.Context, key state.DeliveryKey) (state.PendingDelivery, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	pending, found := s.pending[key]
	return pending, found, nil
}

func (s *memoryDeliveryStore) PendingDeliveries(context.Context) ([]state.PendingDelivery, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	deliveries := make([]state.PendingDelivery, 0, len(s.pending))
	for _, pending := range s.pending {
		deliveries = append(deliveries, pending)
	}
	return deliveries, nil
}

func (s *memoryDeliveryStore) SavePendingDelivery(_ context.Context, pending state.PendingDelivery) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.pending == nil {
		s.pending = make(map[state.DeliveryKey]state.PendingDelivery)
	}
	s.pending[pending.Key] = pending
	return nil
}

func (s *memoryDeliveryStore) DeletePendingDelivery(_ context.Context, key state.DeliveryKey) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.pending, key)
	return nil
}

func (s *memoryDeliveryStore) SourceMessageID(_ context.Context, sourceID int64) (int, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	messageID, ok := s.positions[sourceID]
	return messageID, ok, nil
}

func (s *memoryDeliveryStore) AdvanceSourceMessageID(_ context.Context, sourceID int64, messageID int) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.positions == nil {
		s.positions = make(map[int64]int)
	}
	if messageID > s.positions[sourceID] {
		s.positions[sourceID] = messageID
	}
	return nil
}

type recordingNotifier struct {
	id      string
	err     error
	matches []notifier.Match
}

func (n *recordingNotifier) ID() string { return n.id }

func (n *recordingNotifier) Notify(_ context.Context, match notifier.Match) error {
	n.matches = append(n.matches, match)
	return n.err
}

type recordingTelegramGateway struct {
	calls      []string
	forwardErr error
}

func (g *recordingTelegramGateway) SendText(context.Context, tg.InputPeerClass, string) error {
	g.calls = append(g.calls, "text")
	return nil
}

func (g *recordingTelegramGateway) Forward(context.Context, tg.InputPeerClass, tg.InputPeerClass, int) error {
	g.calls = append(g.calls, "forward")
	return g.forwardErr
}

func TestProcessorRoutesMatchesAndDeduplicatesEdits(t *testing.T) {
	t.Parallel()

	store := &memoryDeliveryStore{delivered: make(map[state.DeliveryKey]bool)}
	first := &recordingNotifier{id: "telegram"}
	second := &recordingNotifier{id: "telegram"}
	sourceValue := source{
		id:   42,
		name: "Market",
		url:  "https://t.me/market_name",
		peer: &tg.InputPeerChannel{ChannelID: 42},
	}
	routes := map[int64][]route{
		42: {
			{ruleName: "laptop", matcher: matcher.New([]string{"MacBook"}), source: sourceValue, notifiers: []notifier.Notifier{first}},
			{ruleName: "computer", matcher: matcher.New([]string{"notebook"}), source: sourceValue, notifiers: []notifier.Notifier{second}},
		},
	}
	value := newProcessor(routes, store, slog.New(slog.NewTextHandler(io.Discard, nil)))
	message := &tg.Message{ID: 7, PeerID: &tg.PeerChannel{ChannelID: 42}, Message: "MACBOOK notebook for sale"}

	value.Handle(t.Context(), message)
	value.Handle(t.Context(), message)

	require.Len(t, first.matches, 1)
	require.Len(t, second.matches, 1)
	require.Equal(t, []string{"MacBook"}, first.matches[0].MatchedTerms)
	messageID, found, err := store.SourceMessageID(t.Context(), 42)
	require.NoError(t, err)
	require.True(t, found)
	require.Equal(t, 7, messageID)
}

func TestProcessorIgnoresUnrelatedSourcesAndUnmatchedText(t *testing.T) {
	t.Parallel()

	store := &memoryDeliveryStore{delivered: make(map[state.DeliveryKey]bool)}
	destination := &recordingNotifier{id: "telegram"}
	value := newProcessor(map[int64][]route{42: {{
		ruleName: "laptop",
		matcher:  matcher.New([]string{"macbook"}),
		source:   source{id: 42, peer: &tg.InputPeerChannel{ChannelID: 42}},
		notifiers: []notifier.Notifier{
			destination,
		},
	}}}, store, slog.New(slog.NewTextHandler(io.Discard, nil)))

	value.Handle(t.Context(), &tg.Message{ID: 1, PeerID: &tg.PeerChannel{ChannelID: 99}, Message: "macbook"})
	value.Handle(t.Context(), &tg.Message{ID: 2, PeerID: &tg.PeerChannel{ChannelID: 42}, Message: "phone"})
	require.Empty(t, destination.matches)
}

func TestProcessorAdvancesPositionForUnmatchedMessages(t *testing.T) {
	t.Parallel()

	store := &memoryDeliveryStore{delivered: make(map[state.DeliveryKey]bool)}
	value := newProcessor(map[int64][]route{42: {{
		ruleName: "laptop",
		matcher:  matcher.New([]string{"macbook"}),
		source:   source{id: 42},
	}}}, store, slog.New(slog.NewTextHandler(io.Discard, nil)))

	value.Handle(t.Context(), &tg.Message{ID: 10, PeerID: &tg.PeerChannel{ChannelID: 42}, Message: "phone"})
	value.Handle(t.Context(), &tg.Message{ID: 8, PeerID: &tg.PeerChannel{ChannelID: 42}, Message: "tablet"})

	messageID, found, err := store.SourceMessageID(t.Context(), 42)
	require.NoError(t, err)
	require.True(t, found)
	require.Equal(t, 10, messageID)
}

func TestProcessorPersistsOnlySuccessfulNotifier(t *testing.T) {
	t.Parallel()

	store := &memoryDeliveryStore{delivered: make(map[state.DeliveryKey]bool)}
	failing := &recordingNotifier{id: "telegram", err: errors.New("failed")}
	successful := &recordingNotifier{id: "macos"}
	value := newProcessor(map[int64][]route{42: {{
		ruleName: "laptop",
		matcher:  matcher.New([]string{"macbook"}),
		source:   source{id: 42, peer: &tg.InputPeerChannel{ChannelID: 42}},
		notifiers: []notifier.Notifier{
			failing,
			successful,
		},
	}}}, store, slog.New(slog.NewTextHandler(io.Discard, nil)))
	message := &tg.Message{ID: 7, PeerID: &tg.PeerChannel{ChannelID: 42}, Message: "macbook"}

	value.Handle(t.Context(), message)
	value.Handle(t.Context(), message)

	require.Len(t, failing.matches, 2)
	require.Len(t, successful.matches, 1)
}

func TestProcessorRetriesOnlyFailedNotifierAfterRestart(t *testing.T) {
	t.Parallel()

	ctx := t.Context()
	statePath := filepath.Join(t.TempDir(), "state.db")
	firstAttempt := time.Date(2026, time.August, 25, 12, 0, 0, 0, time.UTC)
	sourceValue := source{
		id:   42,
		name: "Market",
		url:  "https://t.me/market_name",
		peer: &tg.InputPeerChannel{ChannelID: 42},
	}
	failing := &recordingNotifier{id: "telegram", err: errors.New("temporary failure")}
	successful := &recordingNotifier{id: "macos"}
	routes := map[int64][]route{42: {{
		ruleName:  "laptop",
		matcher:   matcher.New([]string{"macbook"}),
		source:    sourceValue,
		notifiers: []notifier.Notifier{failing, successful},
	}}}

	firstStore, err := state.Open(statePath)
	require.NoError(t, err)
	firstProcessor := newProcessor(routes, firstStore, slog.New(slog.NewTextHandler(io.Discard, nil)))
	firstProcessor.now = func() time.Time { return firstAttempt }
	messageText := "MacBook " + strings.Repeat("x", 400) + " private-tail"
	firstProcessor.Handle(ctx, &tg.Message{
		ID:      7,
		PeerID:  &tg.PeerChannel{ChannelID: 42},
		Message: messageText,
	})
	require.Len(t, failing.matches, 1)
	require.Len(t, successful.matches, 1)
	key := state.DeliveryKey{Rule: "laptop", SourceID: 42, MessageID: 7, NotifierID: "telegram"}
	pending, found, err := firstStore.PendingDelivery(ctx, key)
	require.NoError(t, err)
	require.True(t, found)
	require.Equal(t, 1, pending.Attempts)
	require.Equal(t, firstAttempt.Add(deliveryRetryInitialDelay), pending.NextAttemptTime)
	require.NotEqual(t, messageText, pending.MessageExcerpt)
	require.NotContains(t, pending.MessageExcerpt, "private-tail")
	require.NoError(t, firstStore.Close())

	retryStore, err := state.Open(statePath)
	require.NoError(t, err)
	defer retryStore.Close()
	retried := &recordingNotifier{id: "telegram"}
	alreadyDelivered := &recordingNotifier{id: "macos"}
	retryRoutes := map[int64][]route{42: {{
		ruleName:  "laptop",
		matcher:   matcher.New([]string{"macbook"}),
		source:    sourceValue,
		notifiers: []notifier.Notifier{retried, alreadyDelivered},
	}}}
	retryProcessor := newProcessor(retryRoutes, retryStore, slog.New(slog.NewTextHandler(io.Discard, nil)))
	retryProcessor.now = func() time.Time { return firstAttempt.Add(deliveryRetryInitialDelay) }
	retryProcessor.retryPending(ctx)

	require.Len(t, retried.matches, 1)
	require.Empty(t, alreadyDelivered.matches)
	delivered, err := retryStore.Delivered(ctx, key)
	require.NoError(t, err)
	require.True(t, delivered)
	_, found, err = retryStore.PendingDelivery(ctx, key)
	require.NoError(t, err)
	require.False(t, found)
}

func TestDeliveryRetryDelayIsCapped(t *testing.T) {
	t.Parallel()

	tests := []struct {
		attempts int
		want     time.Duration
	}{
		{attempts: 1, want: 5 * time.Second},
		{attempts: 2, want: 10 * time.Second},
		{attempts: 3, want: 20 * time.Second},
		{attempts: 100, want: 15 * time.Minute},
	}
	for _, test := range tests {
		require.Equal(t, test.want, deliveryRetryDelay(test.attempts))
	}
}
