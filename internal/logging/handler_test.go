package logging

import (
	"bytes"
	"encoding/json"
	"errors"
	"log/slog"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestHandlerFormatsCompactRecord(t *testing.T) {
	var output bytes.Buffer
	handler := newHandler(&output, slog.LevelDebug, false)
	record := slog.NewRecord(
		time.Date(2026, time.August, 26, 14, 7, 34, 0, time.FixedZone("+04", 4*60*60)),
		slog.LevelInfo,
		"observer started",
		0,
	)
	record.Add("sources", 4, "rule", "gaming laptop", "ready", true)

	require.NoError(t, handler.Handle(t.Context(), record))
	const want = "2026-08-26 14:07:34 INF observer started sources=4 rule=\"gaming laptop\" ready=true\n"
	require.Equal(t, want, output.String())
}

func TestHandlerColorsOnlyLevel(t *testing.T) {
	var output bytes.Buffer
	handler := newHandler(&output, slog.LevelInfo, true)
	record := slog.NewRecord(time.Date(2026, time.August, 26, 14, 9, 2, 0, time.UTC), slog.LevelWarn, "notification retry scheduled", 0)
	record.Add("attempts", 2)

	require.NoError(t, handler.Handle(t.Context(), record))
	const want = "2026-08-26 14:09:02 \x1b[33mWRN\x1b[0m notification retry scheduled attempts=2\n"
	require.Equal(t, want, output.String())
}

func TestHandlerEscapesMultilineContent(t *testing.T) {
	var output bytes.Buffer
	handler := newHandler(&output, slog.LevelInfo, false)
	record := slog.NewRecord(time.Date(2026, time.August, 26, 14, 9, 5, 0, time.UTC), slog.LevelError, "notification\nfailed", 0)
	record.Add("error", errors.New("first line\nsecond line"))

	require.NoError(t, handler.Handle(t.Context(), record))
	const want = "2026-08-26 14:09:05 ERR notification\\nfailed error=\"first line\\nsecond line\"\n"
	require.Equal(t, want, output.String())
}

func TestHandlerFormatsGroups(t *testing.T) {
	var output bytes.Buffer
	logger := slog.New(newHandler(&output, slog.LevelInfo, false)).With("component", "observer").WithGroup("delivery").With("rule", "laptop")
	logger.Info("notification delivered", slog.Group("source", "id", int64(-1001)), "message_id", 42)

	require.Contains(t, output.String(), "INF")
	require.Contains(t, output.String(), "component=observer")
	require.Contains(t, output.String(), "delivery.rule=laptop")
	require.Contains(t, output.String(), "delivery.source.id=-1001")
	require.Contains(t, output.String(), "delivery.message_id=42")
}

func TestHandlerFiltersLevels(t *testing.T) {
	var output bytes.Buffer
	logger := slog.New(newHandler(&output, slog.LevelWarn, false))
	logger.Info("observer started")
	logger.Warn("observer stopped")

	require.Contains(t, output.String(), " WRN observer stopped\n")
	require.NotContains(t, output.String(), "observer started")
}

func TestNewFormatsJSONRecord(t *testing.T) {
	var output bytes.Buffer
	logger := New(&output, slog.LevelInfo, FormatJSON)
	logger.Info("observer started", "sources", 4)

	var record struct {
		Level   string `json:"level"`
		Message string `json:"msg"`
		Sources int    `json:"sources"`
	}
	require.NoError(t, json.Unmarshal(output.Bytes(), &record))
	require.Equal(t, "INFO", record.Level)
	require.Equal(t, "observer started", record.Message)
	require.Equal(t, 4, record.Sources)
}
