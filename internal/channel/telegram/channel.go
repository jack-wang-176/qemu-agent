package telegram

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/jack-wang-176/qemu-agent/internal/channel"
)

type Dependencies struct {
	Client Client
	Events *EventSinkFactory
	Logger *slog.Logger
}
type Config struct {
	AllowedUserIDs                                []int64
	AllowGroupChats                               bool
	PollTimeout, RetryMinBackoff, RetryMaxBackoff time.Duration
	MaxConcurrency, MaxInputBytes                 int
}
type Channel struct {
	client  Client
	events  *EventSinkFactory
	logger  *slog.Logger
	cfg     Config
	allowed map[int64]struct{}
}

func New(deps Dependencies, cfg Config) (*Channel, error) {
	if deps.Client == nil || deps.Events == nil || deps.Logger == nil {
		return nil, errors.New("telegram channel dependency is nil")
	}
	if cfg.MaxConcurrency <= 0 || cfg.MaxInputBytes <= 0 || cfg.PollTimeout <= 0 {
		return nil, errors.New("telegram channel config is invalid")
	}
	allowed := make(map[int64]struct{}, len(cfg.AllowedUserIDs))
	for _, id := range cfg.AllowedUserIDs {
		allowed[id] = struct{}{}
	}
	return &Channel{client: deps.Client, events: deps.Events, logger: deps.Logger, cfg: cfg, allowed: allowed}, nil
}
func (*Channel) Name() string { return "telegram" }

func (c *Channel) Run(ctx context.Context, handler channel.Handler) error {
	if handler == nil {
		return errors.New("telegram handler is nil")
	}
	sem := make(chan struct{}, c.cfg.MaxConcurrency)
	var workers sync.WaitGroup
	workerCtx, cancelWorkers := context.WithCancel(ctx)
	defer func() {
		cancelWorkers()
		workers.Wait()
	}()
	offset := int64(0)
	backoff := c.cfg.RetryMinBackoff
	for {
		if err := workerCtx.Err(); err != nil {
			return err
		}
		result, err := c.client.Poll(workerCtx, PollRequest{Offset: offset, Timeout: c.cfg.PollTimeout, Limit: 100})
		if err != nil {
			if !IsRetryable(err) {
				return fmt.Errorf("poll telegram updates: %w", err)
			}
			delay := RetryAfter(err)
			if delay <= 0 {
				delay = backoff
				backoff = min(backoff*2, c.cfg.RetryMaxBackoff)
			}
			timer := time.NewTimer(delay)
			select {
			case <-workerCtx.Done():
				timer.Stop()
				return workerCtx.Err()
			case <-timer.C:
				continue
			}
		}
		backoff = c.cfg.RetryMinBackoff
		if result.NextOffset > offset {
			offset = result.NextOffset
		}
		for _, update := range result.Updates {
			request, target, ok := c.mapUpdate(update)
			if !ok {
				continue
			}
			select {
			case sem <- struct{}{}:
			case <-workerCtx.Done():
				return workerCtx.Err()
			}
			workers.Add(1)
			go func() { defer workers.Done(); defer func() { <-sem }(); c.handle(workerCtx, handler, request, target) }()
		}
	}
}

func (c *Channel) mapUpdate(update Update) (channel.Request, Target, bool) {
	m := update.Message
	if update.ID <= 0 || m == nil || m.From == nil || m.From.IsBot || strings.TrimSpace(m.Text) == "" {
		return channel.Request{}, Target{}, false
	}
	if len([]byte(m.Text)) > c.cfg.MaxInputBytes {
		return channel.Request{}, Target{}, false
	}
	if _, ok := c.allowed[m.From.ID]; !ok {
		return channel.Request{}, Target{}, false
	}
	if m.Chat.Type != "private" && (!c.cfg.AllowGroupChats || (m.Chat.Type != "group" && m.Chat.Type != "supergroup")) {
		return channel.Request{}, Target{}, false
	}
	id := Identity{UserID: m.From.ID, ChatID: m.Chat.ID, ThreadID: m.ThreadID, ChatType: m.Chat.Type}
	return channel.Request{Inbound: channel.Inbound{Channel: c.Name(), SessionKey: id.SessionKey(), UserID: fmt.Sprint(id.UserID), Text: m.Text, Metadata: id.Metadata(update.ID, m.ID)}, Capabilities: channel.Capabilities{InteractiveApproval: false}}, Target{ChatID: m.Chat.ID, ThreadID: m.ThreadID, ReplyTo: m.ID}, true
}

func (c *Channel) handle(ctx context.Context, handler channel.Handler, request channel.Request, target Target) {
	sink, err := c.events.New(target)
	if err != nil {
		c.logger.ErrorContext(ctx, "create telegram request sink", "err", err)
		return
	}
	request.Events = sink
	out, handleErr := handler.Handle(ctx, request)
	finishErr := sink.Finish(ctx)
	err = errors.Join(handleErr, finishErr)
	if err != nil {
		if ctx.Err() == nil && !sink.Rendered() {
			text := "request failed; please try again"
			if channel.IsRecoverable(handleErr) {
				text = handleErr.Error()
			}
			_, _ = c.client.SendMessage(ctx, SendRequest{Target: target, Text: text})
		}
		c.logger.WarnContext(ctx, "telegram request failed", "session_key", request.Inbound.SessionKey, "err", err)
		return
	}
	if out.SessionKey != "" && out.SessionKey != request.Inbound.SessionKey {
		c.logger.ErrorContext(ctx, "telegram handler changed session key")
		return
	}
	if !sink.StreamedText() && out.Text != "" {
		if _, err := c.client.SendMessage(ctx, SendRequest{Target: target, Text: out.Text}); err != nil {
			c.logger.WarnContext(ctx, "send telegram reply", "err", err)
		}
	}
}
