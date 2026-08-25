package notifier

import (
	"context"

	"github.com/gotd/td/tg"
)

type Match struct {
	RuleName     string
	SourceName   string
	SourceURL    string
	SourceID     int64
	SourcePeer   tg.InputPeerClass
	MessageID    int
	Text         string
	MatchedTerms []string
	NoForwards   bool
}

type Notifier interface {
	ID() string
	Notify(context.Context, Match) error
}

// ResumableNotifier can continue a multi-step notification without repeating
// steps that completed before an earlier failure.
type ResumableNotifier interface {
	Notifier
	NotifyFrom(context.Context, Match, string) (checkpoint string, err error)
}
