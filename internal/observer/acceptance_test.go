package observer

import (
	"errors"
	"io"
	"log/slog"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/GeorgeGorbanev/baraholler/internal/config"
	"github.com/GeorgeGorbanev/baraholler/internal/state"
	"github.com/gotd/td/tg"
	"github.com/stretchr/testify/require"
)

func TestOfflineDeliverySurvivesRestartWithoutDuplicateSummary(t *testing.T) {
	t.Parallel()

	ctx := t.Context()
	cfg, err := config.Decode(strings.NewReader(`
telegram:
  api_id: 12345
  api_hash: "test-hash"
  phone: "+995555123456"
  session_path: ".data/telegram.session"
storage:
  state_path: ".data/baraholler.db"
logging:
  level: "info"
  format: "compact"
rules:
  - name: "laptop"
    sources:
      - "https://t.me/market_name"
    terms:
      - "macbook"
    notifications:
      telegram:
        enabled: true
        recipient: "@recipient_username"
      macos:
        enabled: false
`))
	require.NoError(t, err)

	resolver := func() *fakeReferenceResolver {
		return &fakeReferenceResolver{peers: map[string]resolvedPeer{
			"@recipient_username": {
				kind:  peerKindUser,
				id:    99,
				name:  "Recipient",
				input: &tg.InputPeerUser{UserID: 99},
			},
			"https://t.me/market_name": {
				kind:  peerKindChannel,
				id:    42,
				name:  "Market",
				input: &tg.InputPeerChannel{ChannelID: 42},
			},
		}}
	}

	statePath := filepath.Join(t.TempDir(), "state.db")
	firstAttempt := time.Date(2026, time.August, 25, 12, 0, 0, 0, time.UTC)
	firstGateway := &recordingTelegramGateway{forwardErr: errors.New("temporary failure")}
	firstRoutes, err := buildRoutes(ctx, cfg.Rules, resolver(), firstGateway)
	require.NoError(t, err)
	firstStore, err := state.Open(statePath)
	require.NoError(t, err)
	firstProcessor := newProcessor(firstRoutes, firstStore, slog.New(slog.NewTextHandler(io.Discard, nil)))
	firstProcessor.now = func() time.Time { return firstAttempt }
	message := &tg.Message{
		ID:      7,
		PeerID:  &tg.PeerChannel{ChannelID: 42},
		Message: "MacBook for sale",
	}

	firstProcessor.Handle(ctx, message)

	require.Equal(t, []string{"text", "forward"}, firstGateway.calls)
	key := state.DeliveryKey{Rule: "laptop", SourceID: 42, MessageID: 7, NotifierID: "telegram"}
	pending, found, err := firstStore.PendingDelivery(ctx, key)
	require.NoError(t, err)
	require.True(t, found)
	require.NotEmpty(t, pending.NotifierCheckpoint)
	require.NoError(t, firstStore.Close())

	retryGateway := &recordingTelegramGateway{}
	retryRoutes, err := buildRoutes(ctx, cfg.Rules, resolver(), retryGateway)
	require.NoError(t, err)
	retryStore, err := state.Open(statePath)
	require.NoError(t, err)
	defer retryStore.Close()
	retryProcessor := newProcessor(retryRoutes, retryStore, slog.New(slog.NewTextHandler(io.Discard, nil)))
	retryProcessor.now = func() time.Time { return firstAttempt.Add(deliveryRetryInitialDelay) }

	retryProcessor.retryPending(ctx)
	retryProcessor.Handle(ctx, &tg.Message{
		ID:      message.ID,
		PeerID:  message.PeerID,
		Message: "Edited: MacBook is still for sale",
	})

	require.Equal(t, []string{"forward"}, retryGateway.calls)
	delivered, err := retryStore.Delivered(ctx, key)
	require.NoError(t, err)
	require.True(t, delivered)
	_, found, err = retryStore.PendingDelivery(ctx, key)
	require.NoError(t, err)
	require.False(t, found)
}
