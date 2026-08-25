package observer

import (
	"context"
	"errors"
	"testing"

	"github.com/GeorgeGorbanev/baraholler/internal/config"
	"github.com/gotd/td/tg"
	"github.com/stretchr/testify/require"
)

type fakeReferenceResolver struct {
	peers map[string]resolvedPeer
	errs  map[string]error
	calls []string
}

func (f *fakeReferenceResolver) Resolve(_ context.Context, reference string) (resolvedPeer, error) {
	f.calls = append(f.calls, reference)
	if err := f.errs[reference]; err != nil {
		return resolvedPeer{}, err
	}
	peer, ok := f.peers[reference]
	if !ok {
		return resolvedPeer{}, errors.New("unexpected reference")
	}
	return peer, nil
}

type noopTelegramGateway struct{}

func (noopTelegramGateway) SendText(context.Context, tg.InputPeerClass, string) error {
	return nil
}

func (noopTelegramGateway) Forward(context.Context, tg.InputPeerClass, tg.InputPeerClass, int) error {
	return nil
}

func TestBuildRoutesCachesSharedPeersAndPreservesRuleBehavior(t *testing.T) {
	t.Parallel()

	resolver := &fakeReferenceResolver{peers: map[string]resolvedPeer{
		"@recipient_username": {
			kind:  peerKindUser,
			id:    7,
			name:  "Recipient",
			input: &tg.InputPeerUser{UserID: 7},
		},
		"https://t.me/market_name": {
			kind:       peerKindChannel,
			id:         42,
			name:       "Market",
			input:      &tg.InputPeerChannel{ChannelID: 42},
			noForwards: true,
		},
	}}
	rules := []config.Rule{
		telegramRule("laptop", "https://t.me/market_name", "macbook"),
		telegramRule("camera", "https://t.me/MARKET_NAME", "camera"),
	}
	rules[0].Notifications.MacOS.Enabled = true

	routes, err := buildRoutes(t.Context(), rules, resolver, noopTelegramGateway{})
	require.NoError(t, err)
	require.Len(t, resolver.calls, 2)
	require.Len(t, routes[42], 2)
	require.Len(t, routes[42][0].notifiers, 2)
	require.Equal(t, "telegram", routes[42][0].notifiers[0].ID())
	require.Equal(t, "macos", routes[42][0].notifiers[1].ID())
	require.True(t, routes[42][0].source.noForwards)
	require.Equal(t, []string{"camera"}, routes[42][1].matcher.Match("CAMERA for sale"))
}

func TestBuildRoutesDeduplicatesAliasesForTheSameSourceID(t *testing.T) {
	t.Parallel()

	resolver := &fakeReferenceResolver{peers: map[string]resolvedPeer{
		"https://t.me/market_primary": channelPeer(42),
		"https://t.me/market_alias":   channelPeer(42),
	}}
	rule := macOSRule("laptop", []string{
		"https://t.me/market_primary",
		"https://t.me/market_alias",
	})

	routes, err := buildRoutes(t.Context(), []config.Rule{rule}, resolver, noopTelegramGateway{})
	require.NoError(t, err)
	require.Len(t, routes[42], 1)
	require.Len(t, resolver.calls, 2)
}

func TestBuildRoutesRejectsNonUserRecipient(t *testing.T) {
	t.Parallel()

	resolver := &fakeReferenceResolver{peers: map[string]resolvedPeer{
		"@recipient_username": channelPeer(7),
	}}

	_, err := buildRoutes(
		t.Context(),
		[]config.Rule{telegramRule("laptop", "https://t.me/market_name", "macbook")},
		resolver,
		noopTelegramGateway{},
	)
	require.ErrorContains(t, err, `telegram recipient peer invalid: rule="laptop" peer_type=channel expected=user`)
}

func TestBuildRoutesValidatesSourcePeer(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		peer      resolvedPeer
		wantError string
	}{
		{
			name: "user is not a source",
			peer: resolvedPeer{
				kind:  peerKindUser,
				id:    42,
				input: &tg.InputPeerUser{UserID: 42},
			},
			wantError: "source peer invalid",
		},
		{
			name: "account left channel",
			peer: resolvedPeer{
				kind:  peerKindChannel,
				id:    42,
				input: &tg.InputPeerChannel{ChannelID: 42},
				left:  true,
			},
			wantError: "telegram source membership missing",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			resolver := &fakeReferenceResolver{peers: map[string]resolvedPeer{
				"https://t.me/market_name": test.peer,
			}}
			_, err := buildRoutes(
				t.Context(),
				[]config.Rule{macOSRule("laptop", []string{"https://t.me/market_name"})},
				resolver,
				noopTelegramGateway{},
			)
			require.ErrorContains(t, err, test.wantError)
		})
	}
}

func TestBuildRoutesWrapsResolutionErrorWithRuleContext(t *testing.T) {
	t.Parallel()

	resolveErr := errors.New("telegram unavailable")
	resolver := &fakeReferenceResolver{errs: map[string]error{
		"@recipient_username": resolveErr,
	}}
	_, err := buildRoutes(
		t.Context(),
		[]config.Rule{telegramRule("laptop", "https://t.me/market_name", "macbook")},
		resolver,
		noopTelegramGateway{},
	)
	require.ErrorIs(t, err, resolveErr)
	require.ErrorContains(t, err, `telegram recipient resolution failed: rule="laptop"`)
}

func telegramRule(name, sourceURL, term string) config.Rule {
	return config.Rule{
		Name:    name,
		Sources: []string{sourceURL},
		Terms:   []string{term},
		Notifications: config.Notifications{Telegram: config.TelegramNotification{
			Enabled:   true,
			Recipient: "@recipient_username",
		}},
	}
}

func macOSRule(name string, sources []string) config.Rule {
	return config.Rule{
		Name:    name,
		Sources: sources,
		Terms:   []string{"macbook"},
		Notifications: config.Notifications{MacOS: config.MacOSNotification{
			Enabled: true,
		}},
	}
}

func channelPeer(id int64) resolvedPeer {
	return resolvedPeer{
		kind:  peerKindChannel,
		id:    id,
		name:  "Market",
		input: &tg.InputPeerChannel{ChannelID: id},
	}
}
