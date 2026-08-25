package state

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	contribstorage "github.com/gotd/contrib/storage"
	"github.com/gotd/td/telegram/updates"
	"github.com/stretchr/testify/require"
)

func TestStorePersistsDeliveryAndAccessHashes(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "private", "state.db")
	key := DeliveryKey{Rule: "one", SourceID: 42, MessageID: 7, NotifierID: "telegram"}

	store, err := Open(path)
	require.NoError(t, err)
	require.NoError(t, store.MarkDelivered(t.Context(), key))
	require.NoError(t, store.SetChannelAccessHash(t.Context(), 1, 42, 1001))
	require.NoError(t, store.SetUserAccessHash(t.Context(), 1, 99, 2002))
	require.NoError(t, store.AdvanceSourceMessageID(t.Context(), 42, 77))
	require.NoError(t, store.AdvanceSourceMessageID(t.Context(), 42, 70))
	wantUpdateState := updates.State{Pts: 11, Qts: 12, Date: 13, Seq: 14}
	require.NoError(t, store.UpdateState().SetState(t.Context(), 1, wantUpdateState))
	_, err = store.PeerStorage().Resolve(t.Context(), "missing")
	require.ErrorIs(t, err, contribstorage.ErrPeerNotFound)
	require.NoError(t, store.Close())

	store, err = Open(path)
	require.NoError(t, err)
	defer store.Close()
	delivered, err := store.Delivered(t.Context(), key)
	require.NoError(t, err)
	require.True(t, delivered)
	channelHash, found, err := store.GetChannelAccessHash(t.Context(), 1, 42)
	require.NoError(t, err)
	require.True(t, found)
	require.EqualValues(t, 1001, channelHash)
	userHash, found, err := store.GetUserAccessHash(t.Context(), 1, 99)
	require.NoError(t, err)
	require.True(t, found)
	require.EqualValues(t, 2002, userHash)
	messageID, found, err := store.SourceMessageID(t.Context(), 42)
	require.NoError(t, err)
	require.True(t, found)
	require.Equal(t, 77, messageID)
	_, found, err = store.SourceMessageID(t.Context(), 43)
	require.NoError(t, err)
	require.False(t, found)
	gotUpdateState, found, err := store.UpdateState().GetState(t.Context(), 1)
	require.NoError(t, err)
	require.True(t, found)
	require.Equal(t, wantUpdateState, gotUpdateState)

	info, err := os.Stat(path)
	require.NoError(t, err)
	require.Equal(t, os.FileMode(0o600), info.Mode().Perm())
}

func TestDeliveryKeysAreIsolated(t *testing.T) {
	t.Parallel()

	store, err := Open(filepath.Join(t.TempDir(), "state.db"))
	require.NoError(t, err)
	defer store.Close()
	base := DeliveryKey{Rule: "one", SourceID: 42, MessageID: 7, NotifierID: "telegram"}
	require.NoError(t, store.MarkDelivered(t.Context(), base))
	variants := []DeliveryKey{
		{Rule: "two", SourceID: 42, MessageID: 7, NotifierID: "telegram"},
		{Rule: "one", SourceID: 43, MessageID: 7, NotifierID: "telegram"},
		{Rule: "one", SourceID: 42, MessageID: 8, NotifierID: "telegram"},
		{Rule: "one", SourceID: 42, MessageID: 7, NotifierID: "macos"},
	}
	for _, variant := range variants {
		delivered, err := store.Delivered(t.Context(), variant)
		require.NoError(t, err)
		require.False(t, delivered)
	}
}

func TestStorePersistsPendingDeliveries(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "state.db")
	key := DeliveryKey{Rule: "laptop", SourceID: 42, MessageID: 7, NotifierID: "telegram"}
	want := PendingDelivery{
		Key:                key,
		MessageExcerpt:     "MacBook for sale",
		MatchedTerms:       []string{"MacBook"},
		NoForwards:         true,
		NotifierCheckpoint: "summary-sent",
		Attempts:           3,
		NextAttemptTime:    time.Date(2026, time.August, 25, 12, 0, 0, 0, time.UTC),
	}

	store, err := Open(path)
	require.NoError(t, err)
	require.NoError(t, store.SavePendingDelivery(t.Context(), want))
	require.NoError(t, store.Close())

	store, err = Open(path)
	require.NoError(t, err)
	defer store.Close()
	got, found, err := store.PendingDelivery(t.Context(), key)
	require.NoError(t, err)
	require.True(t, found)
	require.Equal(t, want, got)
	deliveries, err := store.PendingDeliveries(t.Context())
	require.NoError(t, err)
	require.Equal(t, []PendingDelivery{want}, deliveries)
	require.NoError(t, store.DeletePendingDelivery(t.Context(), key))
	_, found, err = store.PendingDelivery(t.Context(), key)
	require.NoError(t, err)
	require.False(t, found)
}
