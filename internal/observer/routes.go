package observer

import (
	"context"
	"fmt"
	"strings"

	"github.com/GeorgeGorbanev/baraholler/internal/config"
	"github.com/GeorgeGorbanev/baraholler/internal/matcher"
	"github.com/GeorgeGorbanev/baraholler/internal/notifier"
	barahollerstate "github.com/GeorgeGorbanev/baraholler/internal/state"
	contribstorage "github.com/gotd/contrib/storage"
	"github.com/gotd/td/telegram/message"
	messagepeer "github.com/gotd/td/telegram/message/peer"
	"github.com/gotd/td/telegram/peers"
	"github.com/gotd/td/tg"
)

type peerKind string

const (
	peerKindUnknown peerKind = "unknown peer"
	peerKindUser    peerKind = "user"
	peerKindChat    peerKind = "chat"
	peerKindChannel peerKind = "channel"
)

type resolvedPeer struct {
	kind       peerKind
	id         int64
	name       string
	input      tg.InputPeerClass
	left       bool
	noForwards bool
}

type referenceResolver interface {
	Resolve(context.Context, string) (resolvedPeer, error)
}

type gotdReferenceResolver struct {
	resolver messagepeer.Resolver
	manager  *peers.Manager
}

func (r gotdReferenceResolver) Resolve(ctx context.Context, reference string) (resolvedPeer, error) {
	input, err := messagepeer.Resolve(reference).Bind(r.resolver)(ctx)
	if err != nil {
		return resolvedPeer{}, fmt.Errorf("reference resolution failed: %w", err)
	}
	value, err := r.manager.FromInputPeer(ctx, input)
	if err != nil {
		return resolvedPeer{}, fmt.Errorf("peer load failed: %w", err)
	}

	result := resolvedPeer{
		id:    value.ID(),
		name:  value.VisibleName(),
		input: value.InputPeer(),
	}
	switch peer := value.(type) {
	case peers.User:
		result.kind = peerKindUser
	case peers.Chat:
		result.kind = peerKindChat
		result.left = peer.Left()
		result.noForwards = peer.NoForwards()
	case peers.Channel:
		result.kind = peerKindChannel
		result.left = peer.Left()
		result.noForwards = peer.NoForwards()
	default:
		result.kind = peerKindUnknown
	}
	return result, nil
}

func (o *Observer) resolveRoutes(
	ctx context.Context,
	api *tg.Client,
	store *barahollerstate.Store,
) (map[int64][]route, error) {
	resolver := contribstorage.NewResolverCache(messagepeer.Plain(api), store.PeerStorage())
	peerManager := peers.Options{}.Build(api)
	sender := message.NewSender(api).WithResolver(resolver)
	gateway := notifier.NewGotdGateway(sender)
	return buildRoutes(ctx, o.config.Rules, gotdReferenceResolver{
		resolver: resolver,
		manager:  peerManager,
	}, gateway)
}

func buildRoutes(
	ctx context.Context,
	rules []config.Rule,
	resolver referenceResolver,
	gateway notifier.TelegramGateway,
) (map[int64][]route, error) {
	recipients := make(map[string]tg.InputPeerClass)
	sources := make(map[string]source)
	routes := make(map[int64][]route)

	for _, ruleConfig := range rules {
		var destinations []notifier.Notifier
		if ruleConfig.Notifications.Telegram.Enabled {
			reference, err := config.RecipientReference(ruleConfig.Notifications.Telegram.Recipient)
			if err != nil {
				return nil, fmt.Errorf("telegram recipient validation failed: rule=%q: %w", ruleConfig.Name, err)
			}
			recipient, ok := recipients[reference]
			if !ok {
				peer, err := resolver.Resolve(ctx, reference)
				if err != nil {
					return nil, fmt.Errorf("telegram recipient resolution failed: rule=%q: %w", ruleConfig.Name, err)
				}
				if peer.kind != peerKindUser {
					return nil, fmt.Errorf("telegram recipient peer invalid: rule=%q peer_type=%s expected=user", ruleConfig.Name, peer.kind)
				}
				recipient = peer.input
				recipients[reference] = recipient
			}
			destinations = append(destinations, notifier.NewTelegram(recipient, gateway))
		}
		if ruleConfig.Notifications.MacOS.Enabled {
			destinations = append(destinations, notifier.NewMacOS(nil))
		}

		ruleMatcher := matcher.New(ruleConfig.Terms)
		seenSourceIDs := make(map[int64]struct{}, len(ruleConfig.Sources))
		for _, sourceURL := range ruleConfig.Sources {
			username, err := config.SourceUsername(sourceURL)
			if err != nil {
				return nil, fmt.Errorf("source validation failed: source=%q rule=%q: %w", sourceURL, ruleConfig.Name, err)
			}
			canonicalURL := "https://t.me/" + username
			sourceValue, ok := sources[strings.ToLower(username)]
			if !ok {
				peer, err := resolver.Resolve(ctx, canonicalURL)
				if err != nil {
					return nil, fmt.Errorf("source resolution failed: source=%q: %w", canonicalURL, err)
				}
				if peer.kind != peerKindChannel {
					return nil, fmt.Errorf("source peer invalid: source=%q peer_type=%s expected=channel_or_supergroup", canonicalURL, peer.kind)
				}
				if peer.left {
					return nil, fmt.Errorf("telegram source membership missing: source=%q", canonicalURL)
				}
				sourceValue = source{
					id:         peer.id,
					name:       peer.name,
					url:        canonicalURL,
					peer:       peer.input,
					noForwards: peer.noForwards,
				}
				sources[strings.ToLower(username)] = sourceValue
			}
			if _, exists := seenSourceIDs[sourceValue.id]; exists {
				continue
			}
			seenSourceIDs[sourceValue.id] = struct{}{}
			routes[sourceValue.id] = append(routes[sourceValue.id], route{
				ruleName:  ruleConfig.Name,
				matcher:   ruleMatcher,
				source:    sourceValue,
				notifiers: destinations,
			})
		}
	}
	return routes, nil
}
