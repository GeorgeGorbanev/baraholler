package notifier

import (
	"context"
	"errors"
	"fmt"

	"github.com/gotd/td/telegram/message"
	"github.com/gotd/td/tg"
	"github.com/gotd/td/tgerr"
)

var ErrForwardsRestricted = errors.New("telegram content forwarding restricted")

const telegramSummarySentCheckpoint = "summary-sent"

type TelegramGateway interface {
	SendText(context.Context, tg.InputPeerClass, string) error
	Forward(context.Context, tg.InputPeerClass, tg.InputPeerClass, int) error
}

type GotdGateway struct {
	sender *message.Sender
}

func NewGotdGateway(sender *message.Sender) *GotdGateway {
	return &GotdGateway{sender: sender}
}

func (g *GotdGateway) SendText(ctx context.Context, recipient tg.InputPeerClass, text string) error {
	if _, err := g.sender.To(recipient).Text(ctx, text); err != nil {
		return fmt.Errorf("telegram text send failed: %w", err)
	}
	return nil
}

func (g *GotdGateway) Forward(
	ctx context.Context,
	recipient tg.InputPeerClass,
	source tg.InputPeerClass,
	messageID int,
) error {
	if _, err := g.sender.To(recipient).ForwardIDs(source, messageID).Send(ctx); err != nil {
		if tgerr.Is(err, "CHAT_FORWARDS_RESTRICTED") {
			return fmt.Errorf("%w: %v", ErrForwardsRestricted, err)
		}
		return fmt.Errorf("telegram message forward failed: %w", err)
	}
	return nil
}

type Telegram struct {
	recipient tg.InputPeerClass
	gateway   TelegramGateway
}

func NewTelegram(recipient tg.InputPeerClass, gateway TelegramGateway) *Telegram {
	return &Telegram{recipient: recipient, gateway: gateway}
}

func (*Telegram) ID() string {
	return "telegram"
}

func (n *Telegram) Notify(ctx context.Context, match Match) error {
	_, err := n.NotifyFrom(ctx, match, "")
	return err
}

func (n *Telegram) NotifyFrom(ctx context.Context, match Match, checkpoint string) (string, error) {
	if checkpoint != "" && checkpoint != telegramSummarySentCheckpoint {
		return checkpoint, fmt.Errorf("telegram notification checkpoint invalid: %q", checkpoint)
	}

	if match.NoForwards {
		fallback := Summary(match, true)
		if checkpoint == telegramSummarySentCheckpoint {
			fallback = "Telegram blocked the forward.\n" + fallback
		}
		if err := n.gateway.SendText(ctx, n.recipient, fallback); err != nil {
			return checkpoint, fmt.Errorf("protected-content fallback send failed: %w", err)
		}
		return checkpoint, nil
	}

	if checkpoint == "" {
		if err := n.gateway.SendText(ctx, n.recipient, Summary(match, false)); err != nil {
			return "", fmt.Errorf("match summary send failed: %w", err)
		}
		checkpoint = telegramSummarySentCheckpoint
	}
	if err := n.gateway.Forward(ctx, n.recipient, match.SourcePeer, match.MessageID); err != nil {
		if errors.Is(err, ErrForwardsRestricted) {
			fallback := "Telegram blocked the forward.\n" + Summary(match, true)
			if fallbackErr := n.gateway.SendText(ctx, n.recipient, fallback); fallbackErr != nil {
				return checkpoint, fmt.Errorf("restricted-forward fallback send failed: %w", errors.Join(err, fallbackErr))
			}
			return checkpoint, nil
		}
		return checkpoint, err
	}
	return checkpoint, nil
}
