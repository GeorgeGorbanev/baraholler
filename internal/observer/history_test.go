package observer

import (
	"context"
	"io"
	"log/slog"
	"testing"

	"github.com/GeorgeGorbanev/baraholler/internal/matcher"
	"github.com/GeorgeGorbanev/baraholler/internal/notifier"
	"github.com/GeorgeGorbanev/baraholler/internal/state"
	"github.com/gotd/td/tg"
	"github.com/stretchr/testify/require"
)

type historyCall struct {
	offsetID int
	minID    int
	limit    int
}

type fakeHistoryAPI struct {
	pages [][]tg.MessageClass
	calls []historyCall
}

func (f *fakeHistoryAPI) GetHistory(
	_ context.Context,
	_ tg.InputPeerClass,
	offsetID int,
	minID int,
	limit int,
) ([]tg.MessageClass, error) {
	f.calls = append(f.calls, historyCall{offsetID: offsetID, minID: minID, limit: limit})
	if len(f.pages) == 0 {
		return nil, nil
	}
	page := f.pages[0]
	f.pages = f.pages[1:]
	return page, nil
}

func TestHistoryRecovererInitializesNewSourceAtLatestMessage(t *testing.T) {
	t.Parallel()

	history := &fakeHistoryAPI{pages: [][]tg.MessageClass{{
		&tg.Message{ID: 77, PeerID: &tg.PeerChannel{ChannelID: 42}, Message: "old macbook"},
	}}}
	store := &memoryDeliveryStore{delivered: make(map[state.DeliveryKey]bool)}
	routes := testHistoryRoutes(&recordingNotifier{id: "telegram"})
	processor := newProcessor(routes, store, discardLogger())
	recoverer := newHistoryRecoverer(history, routes, processor, store, discardLogger())

	require.NoError(t, recoverer.Initialize(t.Context()))
	messageID, found, err := store.SourceMessageID(t.Context(), 42)
	require.NoError(t, err)
	require.True(t, found)
	require.Equal(t, 77, messageID)
	require.Equal(t, []historyCall{{limit: 1}}, history.calls)
}

func TestHistoryRecovererKeepsExistingSourcePosition(t *testing.T) {
	t.Parallel()

	history := &fakeHistoryAPI{}
	store := &memoryDeliveryStore{
		delivered: make(map[state.DeliveryKey]bool),
		positions: map[int64]int{42: 50},
	}
	routes := testHistoryRoutes(&recordingNotifier{id: "telegram"})
	recoverer := newHistoryRecoverer(history, routes, newProcessor(routes, store, discardLogger()), store, discardLogger())

	require.NoError(t, recoverer.Initialize(t.Context()))
	require.Empty(t, history.calls)
}

func TestHistoryRecovererPagesAndProcessesOldestFirst(t *testing.T) {
	t.Parallel()

	firstPage := historyMessages(250, 151)
	secondPage := historyMessages(150, 101)
	history := &fakeHistoryAPI{pages: [][]tg.MessageClass{firstPage, secondPage}}
	destination := &recordingNotifier{id: "telegram"}
	store := &memoryDeliveryStore{
		delivered: make(map[state.DeliveryKey]bool),
		positions: map[int64]int{42: 100},
	}
	routes := testHistoryRoutes(destination)
	recoverer := newHistoryRecoverer(history, routes, newProcessor(routes, store, discardLogger()), store, discardLogger())

	require.NoError(t, recoverer.recover(t.Context(), historyRecoveryRequest{sourceID: 42, afterMessageID: 100}))
	require.Len(t, destination.matches, 150)
	for i, match := range destination.matches {
		want := 101 + i
		require.Equal(t, want, match.MessageID)
	}
	wantCalls := []historyCall{
		{offsetID: 0, minID: 100, limit: historyPageSize},
		{offsetID: 151, minID: 100, limit: historyPageSize},
	}
	require.Equal(t, wantCalls, history.calls)
	messageID, found, err := store.SourceMessageID(t.Context(), 42)
	require.NoError(t, err)
	require.True(t, found)
	require.Equal(t, 250, messageID)
}

func TestHistoryRecovererDeduplicatesQueuedSource(t *testing.T) {
	t.Parallel()

	store := &memoryDeliveryStore{
		delivered: make(map[state.DeliveryKey]bool),
		positions: map[int64]int{42: 100},
	}
	routes := testHistoryRoutes(&recordingNotifier{id: "telegram"})
	recoverer := newHistoryRecoverer(&fakeHistoryAPI{}, routes, newProcessor(routes, store, discardLogger()), store, discardLogger())

	configured, err := recoverer.Enqueue(t.Context(), 42)
	require.NoError(t, err)
	require.True(t, configured)
	configured, err = recoverer.Enqueue(t.Context(), 42)
	require.NoError(t, err)
	require.True(t, configured)
	require.NoError(t, store.AdvanceSourceMessageID(t.Context(), 42, 200))
	require.Len(t, recoverer.requests, 1)
	request := <-recoverer.requests
	require.Equal(t, 100, request.afterMessageID)
	configured, err = recoverer.Enqueue(t.Context(), 99)
	require.NoError(t, err)
	require.False(t, configured)
}

func testHistoryRoutes(destination notifier.Notifier) map[int64][]route {
	sourceValue := source{
		id:   42,
		name: "Market",
		url:  "https://t.me/market_name",
		peer: &tg.InputPeerChannel{ChannelID: 42},
	}
	return map[int64][]route{42: {{
		ruleName:  "laptop",
		matcher:   matcher.New([]string{"macbook"}),
		source:    sourceValue,
		notifiers: []notifier.Notifier{destination},
	}}}
}

func historyMessages(high, low int) []tg.MessageClass {
	messages := make([]tg.MessageClass, 0, high-low+1)
	for messageID := high; messageID >= low; messageID-- {
		messages = append(messages, &tg.Message{
			ID:      messageID,
			PeerID:  &tg.PeerChannel{ChannelID: 42},
			Message: "macbook",
		})
	}
	return messages
}

func discardLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}
