package observer

import (
	"context"
	"testing"

	"github.com/gotd/td/telegram"
	"github.com/gotd/td/tg"
	"github.com/stretchr/testify/require"
)

type recordingUpdateHandler struct {
	updates []tg.UpdatesClass
}

func (h *recordingUpdateHandler) Handle(_ context.Context, update tg.UpdatesClass) error {
	h.updates = append(h.updates, update)
	return nil
}

func TestUpdateBufferReplaysThenPassesThrough(t *testing.T) {
	t.Parallel()

	buffer := &updateBuffer{}
	first := &tg.UpdateShort{}
	second := &tg.UpdateShort{}
	third := &tg.UpdateShort{}
	require.NoError(t, buffer.Handle(t.Context(), first))
	require.NoError(t, buffer.Handle(t.Context(), second))
	handler := &recordingUpdateHandler{}
	require.NoError(t, buffer.Activate(t.Context(), handler))
	require.NoError(t, buffer.Handle(t.Context(), third))
	require.Equal(t, []tg.UpdatesClass{first, second, third}, handler.updates)
	var _ telegram.UpdateHandler = buffer
}
