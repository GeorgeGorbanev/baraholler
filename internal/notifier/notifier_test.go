package notifier

import (
	"context"
	"errors"
	"testing"

	"github.com/gotd/td/tg"
	"github.com/stretchr/testify/require"
)

type gatewayCall struct {
	kind string
	text string
}

type fakeGateway struct {
	calls      []gatewayCall
	forwardErr error
}

func (g *fakeGateway) SendText(_ context.Context, _ tg.InputPeerClass, text string) error {
	g.calls = append(g.calls, gatewayCall{kind: "text", text: text})
	return nil
}

func (g *fakeGateway) Forward(context.Context, tg.InputPeerClass, tg.InputPeerClass, int) error {
	g.calls = append(g.calls, gatewayCall{kind: "forward"})
	return g.forwardErr
}

func TestTelegramSendsSummaryThenForward(t *testing.T) {
	t.Parallel()

	gateway := &fakeGateway{}
	value := NewTelegram(&tg.InputPeerUser{UserID: 1}, gateway)
	require.NoError(t, value.Notify(t.Context(), testMatch()))
	require.Len(t, gateway.calls, 2)
	require.Equal(t, []string{"text", "forward"}, []string{gateway.calls[0].kind, gateway.calls[1].kind})
	require.Contains(t, gateway.calls[0].text, `rule "laptop"`)
	require.NotContains(t, gateway.calls[0].text, "Message:")
}

func TestTelegramResumesForwardWithoutRepeatingSummary(t *testing.T) {
	t.Parallel()

	gateway := &fakeGateway{forwardErr: errors.New("temporary failure")}
	value := NewTelegram(&tg.InputPeerUser{UserID: 1}, gateway)
	checkpoint, err := value.NotifyFrom(t.Context(), testMatch(), "")
	require.Error(t, err)
	require.Equal(t, telegramSummarySentCheckpoint, checkpoint)

	gateway.forwardErr = nil
	_, err = value.NotifyFrom(t.Context(), testMatch(), checkpoint)
	require.NoError(t, err)
	want := []string{"text", "forward", "forward"}
	got := make([]string, 0, len(gateway.calls))
	for _, call := range gateway.calls {
		got = append(got, call.kind)
	}
	require.Equal(t, want, got)
}

func TestTelegramProtectedContentUsesTextFallback(t *testing.T) {
	t.Parallel()

	match := testMatch()
	match.NoForwards = true
	gateway := &fakeGateway{}
	value := NewTelegram(&tg.InputPeerUser{UserID: 1}, gateway)
	require.NoError(t, value.Notify(t.Context(), match))
	require.Len(t, gateway.calls, 1)
	require.Equal(t, "text", gateway.calls[0].kind)
	require.Contains(t, gateway.calls[0].text, "Message: Great laptop")
	require.Contains(t, gateway.calls[0].text, "https://t.me/source_name/7")
}

func TestTelegramFallsBackWhenForwardIsRejected(t *testing.T) {
	t.Parallel()

	gateway := &fakeGateway{forwardErr: ErrForwardsRestricted}
	value := NewTelegram(&tg.InputPeerUser{UserID: 1}, gateway)
	require.NoError(t, value.Notify(t.Context(), testMatch()))
	require.Len(t, gateway.calls, 3)
	require.Equal(t, []string{"text", "forward", "text"}, []string{gateway.calls[0].kind, gateway.calls[1].kind, gateway.calls[2].kind})
}

type fakeRunner struct {
	name string
	args []string
	err  error
}

func (r *fakeRunner) Run(_ context.Context, name string, args ...string) error {
	r.name = name
	r.args = append([]string(nil), args...)
	return r.err
}

func TestMacOSPassesContentAsArguments(t *testing.T) {
	t.Parallel()

	runner := &fakeRunner{}
	match := testMatch()
	match.RuleName = `rule" & do shell script "bad" & "`
	require.NoError(t, NewMacOS(runner).Notify(t.Context(), match))
	require.Equal(t, "/usr/bin/osascript", runner.name)
	require.Len(t, runner.args, 5)
	require.Equal(t, []string{"-e", macOSScript, "--"}, runner.args[:3])
	require.Contains(t, runner.args[3], "do shell script")

	runner.err = errors.New("failed")
	require.Error(t, NewMacOS(runner).Notify(t.Context(), match))
}

func testMatch() Match {
	return Match{
		RuleName:     "laptop",
		SourceName:   "Source",
		SourceURL:    "https://t.me/source_name",
		SourceID:     42,
		SourcePeer:   &tg.InputPeerChannel{ChannelID: 42},
		MessageID:    7,
		Text:         "Great laptop",
		MatchedTerms: []string{"laptop"},
	}
}
