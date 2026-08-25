package observer

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync/atomic"
	"time"

	"github.com/GeorgeGorbanev/baraholler/internal/config"
	barahollerstate "github.com/GeorgeGorbanev/baraholler/internal/state"
	"github.com/gotd/contrib/middleware/floodwait"
	"github.com/gotd/contrib/middleware/ratelimit"
	contribstorage "github.com/gotd/contrib/storage"
	"github.com/gotd/td/session"
	"github.com/gotd/td/telegram"
	"github.com/gotd/td/telegram/auth"
	"github.com/gotd/td/telegram/updates"
	updhook "github.com/gotd/td/telegram/updates/hook"
	"github.com/gotd/td/tg"
	"golang.org/x/sync/errgroup"
	"golang.org/x/time/rate"
)

type Options struct {
	Config  config.Config
	Logger  *slog.Logger
	Input   io.Reader
	Output  io.Writer
	Version string
}

type Observer struct {
	config  config.Config
	logger  *slog.Logger
	input   io.Reader
	output  io.Writer
	version string
}

func New(options Options) (*Observer, error) {
	options.Config.Normalize()
	if err := options.Config.ValidateForOS(runtime.GOOS); err != nil {
		return nil, fmt.Errorf("observer config validation failed: %w", err)
	}
	if options.Logger == nil {
		return nil, errors.New("observer logger missing")
	}
	if options.Input == nil {
		options.Input = os.Stdin
	}
	if options.Output == nil {
		options.Output = os.Stdout
	}
	if strings.TrimSpace(options.Version) == "" {
		options.Version = "dev"
	}
	return &Observer{
		config:  options.Config,
		logger:  options.Logger,
		input:   options.Input,
		output:  options.Output,
		version: options.Version,
	}, nil
}

func (o *Observer) Run(ctx context.Context) error {
	if err := ensurePrivateParent(o.config.Telegram.SessionPath); err != nil {
		return err
	}
	store, err := barahollerstate.Open(o.config.Storage.StatePath)
	if err != nil {
		return err
	}
	defer func() {
		if err := store.Close(); err != nil {
			o.logger.Error("state database close failed", "error", err)
		}
	}()

	dispatcher := tg.NewUpdateDispatcher()
	collector := contribstorage.UpdateHook(dispatcher, store.PeerStorage())
	var recoverer atomic.Pointer[historyRecoverer]
	gaps := updates.New(updates.Config{
		Handler:          collector,
		Storage:          store.UpdateState(),
		AccessHasher:     store,
		UserAccessHasher: store,
		OnChannelTooLong: func(channelID int64) {
			if value := recoverer.Load(); value != nil {
				configured, err := value.Enqueue(ctx, channelID)
				if err != nil {
					o.logger.Error("history recovery scheduling failed",
						"channel_id", channelID,
						"reason", "channel_update_gap",
						"error", err,
					)
					return
				}
				if configured {
					o.logger.Warn("history recovery scheduled",
						"channel_id", channelID,
						"reason", "channel_update_gap",
					)
					return
				}
			}
			o.logger.Warn("history recovery skipped",
				"channel_id", channelID,
				"reason", "source_not_configured",
			)
		},
		OnChannelInaccessible: func(channelID int64) {
			o.logger.Error("telegram channel inaccessible", "channel_id", channelID)
		},
		OnTooLong: func() {
			o.logger.Warn("telegram account recovery incomplete", "reason", "update_gap")
		},
		OnLoadUserStateFailed: func() {
			o.logger.Info("telegram update state initialized", "initial_history_scan", false)
		},
		OnLoadChannelStateFailed: func(channelID int64) {
			o.logger.Warn("channel recovery state load failed", "channel_id", channelID)
		},
		MaxChannelDifferenceConcurrency: 4,
	})

	buffer := &updateBuffer{}
	waiter := floodwait.NewWaiter().WithCallback(func(_ context.Context, wait floodwait.FloodWait) {
		o.logger.Warn("telegram request retry scheduled", "wait", wait.Duration)
	})
	client := telegram.NewClient(o.config.Telegram.APIID, o.config.Telegram.APIHash, telegram.Options{
		SessionStorage: &session.FileStorage{Path: o.config.Telegram.SessionPath},
		UpdateHandler:  buffer,
		Middlewares: []telegram.Middleware{
			waiter,
			ratelimit.New(rate.Every(100*time.Millisecond), 5),
			updhook.UpdateHook(buffer.Handle),
			updhook.AffectedHook(gaps),
		},
		Device: telegram.DeviceConfig{
			DeviceModel:    "Baraholler",
			SystemVersion:  runtime.GOOS,
			AppVersion:     o.version,
			SystemLangCode: "en",
			LangCode:       "en",
		},
	})

	o.logger.Info("baraholler started")
	err = waiter.Run(ctx, func(ctx context.Context) error {
		return client.Run(ctx, func(ctx context.Context) error {
			authenticator := newTerminalAuthenticator(o.config.Telegram.Phone, o.input, o.output)
			flow := auth.NewFlow(authenticator, auth.SendCodeOptions{})
			if err := client.Auth().IfNecessary(ctx, flow); err != nil {
				return fmt.Errorf("telegram account authorization failed: %w", err)
			}
			self, err := client.Self(ctx)
			if err != nil {
				return fmt.Errorf("telegram account fetch failed: %w", err)
			}

			routes, err := o.resolveRoutes(ctx, client.API(), store)
			if err != nil {
				return err
			}
			processor := newProcessor(routes, store, o.logger)
			historyRecovery := newHistoryRecoverer(telegramHistoryAPI{client: client.API()}, routes, processor, store, o.logger)
			if err := historyRecovery.Initialize(ctx); err != nil {
				return fmt.Errorf("telegram history recovery initialization failed: %w", err)
			}
			recoverer.Store(historyRecovery)
			registerHandlers(dispatcher, processor)

			updatesCtx, cancelUpdates := context.WithCancel(ctx)
			defer cancelUpdates()
			group, groupCtx := errgroup.WithContext(updatesCtx)
			started := make(chan struct{})
			group.Go(func() error {
				return gaps.Run(groupCtx, client.API(), self.ID, updates.AuthOptions{
					IsBot: self.Bot,
					OnStart: func(context.Context) {
						close(started)
					},
				})
			})
			group.Go(func() error {
				return historyRecovery.Run(groupCtx)
			})
			group.Go(func() error {
				return processor.RunRetries(groupCtx)
			})

			select {
			case <-started:
			case <-groupCtx.Done():
				return group.Wait()
			}
			if err := buffer.Activate(groupCtx, gaps); err != nil {
				return fmt.Errorf("telegram update processing activation failed: %w", err)
			}
			o.logger.Info("observer started", "sources", len(routes), "account_id", self.ID)
			return group.Wait()
		})
	})
	if errors.Is(err, context.Canceled) || (errors.Is(err, context.DeadlineExceeded) && ctx.Err() != nil) {
		o.logger.Info("baraholler stopped")
		return nil
	}
	if err != nil {
		return fmt.Errorf("telegram observer run failed: %w", err)
	}
	o.logger.Info("baraholler stopped")
	return nil
}

func registerHandlers(dispatcher tg.UpdateDispatcher, processor *processor) {
	handle := func(ctx context.Context, message tg.MessageClass) {
		if value, ok := message.(*tg.Message); ok {
			processor.Handle(ctx, value)
		}
	}
	dispatcher.OnNewMessage(func(ctx context.Context, _ tg.Entities, update *tg.UpdateNewMessage) error {
		handle(ctx, update.Message)
		return nil
	})
	dispatcher.OnEditMessage(func(ctx context.Context, _ tg.Entities, update *tg.UpdateEditMessage) error {
		handle(ctx, update.Message)
		return nil
	})
	dispatcher.OnNewChannelMessage(func(ctx context.Context, _ tg.Entities, update *tg.UpdateNewChannelMessage) error {
		handle(ctx, update.Message)
		return nil
	})
	dispatcher.OnEditChannelMessage(func(ctx context.Context, _ tg.Entities, update *tg.UpdateEditChannelMessage) error {
		handle(ctx, update.Message)
		return nil
	})
}

func ensurePrivateParent(path string) error {
	directory := filepath.Dir(path)
	_, statErr := os.Stat(directory)
	if err := os.MkdirAll(directory, 0o700); err != nil {
		return fmt.Errorf("session directory creation failed: %w", err)
	}
	if errors.Is(statErr, os.ErrNotExist) {
		if err := os.Chmod(directory, 0o700); err != nil {
			return fmt.Errorf("session directory permission update failed: %w", err)
		}
	}
	return nil
}
