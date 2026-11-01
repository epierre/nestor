// Package telegram connects users to the assistant through the Telegram Bot API. It is the only
// package that handles Telegram identities: everything it calls sees an internal user ID.
package telegram

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"strconv"
	"time"

	"github.com/epierre/nestor/internal/channel"
	"github.com/epierre/nestor/internal/identity"
	"github.com/epierre/nestor/internal/store"
)

const (
	defaultBaseURL = "https://api.telegram.org"
	platform       = "telegram"

	pollSeconds = 50
	pollMargin  = 10 * time.Second // on top of the long poll, for the response to arrive
	sendTimeout = 30 * time.Second

	defaultMinBackoff = time.Second
	defaultMaxBackoff = time.Minute

	noticeFailure = "Something went wrong, try again shortly."
)

var _ channel.Channel = (*Channel)(nil)

// Identities resolves a platform identity to the internal user behind it. It returns
// store.ErrIdentityNotFound for one that is unknown or disabled.
type Identities interface {
	ResolveIdentity(ctx context.Context, platform, externalID string) (identity.UserID, error)
}

// Channel long-polls Telegram for messages. The bot token is part of every request URL, so errors
// from the HTTP client must pass through call before they reach a log.
type Channel struct {
	baseURL    string
	token      string
	identities Identities
	http       *http.Client
	minBackoff time.Duration
	maxBackoff time.Duration
}

// New returns a Channel for the bot with token.
func New(token string, identities Identities) *Channel {
	return &Channel{
		baseURL:    defaultBaseURL,
		token:      token,
		identities: identities,
		http:       &http.Client{},
		minBackoff: defaultMinBackoff,
		maxBackoff: defaultMaxBackoff,
	}
}

// Run delivers messages to h until ctx is cancelled, then returns nil.
//
// It fetches one update at a time, because Telegram confirms updates by the offset of the next
// request: from a batch, updates handled before a crash would be delivered again. An update is
// confirmed only once it has been handled, or its failure reported to the sender.
func (c *Channel) Run(ctx context.Context, h channel.Handler) error {
	var offset int64
	failures := 0
	for ctx.Err() == nil {
		next, err := c.poll(ctx, h, offset)
		if ctx.Err() != nil {
			break
		}
		if err != nil {
			failures++
			delay := c.backoff(failures)
			slog.WarnContext(ctx, "telegram poll failed", "error", err, "retry_in", delay.String())
			if !sleep(ctx, delay) {
				break
			}
			continue
		}
		failures = 0
		offset = next
	}
	return nil
}

// poll handles the next update, if any, and returns the offset that confirms it.
func (c *Channel) poll(ctx context.Context, h channel.Handler, offset int64) (int64, error) {
	updates, err := c.getUpdates(ctx, offset)
	if err != nil {
		return offset, err
	}
	for _, u := range updates {
		if err := c.handle(ctx, h, u); err != nil {
			return offset, err
		}
		offset = u.UpdateID + 1
	}
	return offset, nil
}

// handle returns an error when the update must stay unconfirmed, to be delivered again.
func (c *Channel) handle(ctx context.Context, h channel.Handler, u update) error {
	if u.Message == nil || u.Message.From == nil {
		return nil
	}
	m := u.Message
	user, err := c.identities.ResolveIdentity(ctx, platform, strconv.FormatInt(m.From.ID, 10))
	if errors.Is(err, store.ErrIdentityNotFound) {
		slog.WarnContext(ctx, "ignored message from unknown sender", "telegram_id", m.From.ID)
		return nil
	}
	if err != nil {
		// Nothing can be sent: the sender may be a stranger.
		return fmt.Errorf("resolve sender: %w", err)
	}

	reply, err := h.HandleMessage(identity.WithUser(ctx, user), m.Text)
	if err != nil {
		slog.ErrorContext(ctx, "message handling failed", "error", err)
		reply = noticeFailure
	}
	err = c.Send(ctx, m.Chat.ID, reply)
	if isPermanent(err) {
		slog.ErrorContext(ctx, "reply refused", "error", err)
		return nil
	}
	return err
}

// Send delivers text to the chat, retrying transient failures until ctx is cancelled.
func (c *Channel) Send(ctx context.Context, chatID int64, text string) error {
	payload := sendMessageRequest{ChatID: chatID, Text: text}
	for failures := 1; ; failures++ {
		sendCtx, cancel := context.WithTimeout(ctx, sendTimeout)
		err := c.do(sendCtx, "sendMessage", payload, nil)
		cancel()
		if err == nil || isPermanent(err) {
			return err
		}
		delay := c.backoff(failures)
		slog.WarnContext(ctx, "telegram send failed", "error", err, "retry_in", delay.String())
		if !sleep(ctx, delay) {
			return fmt.Errorf("send message: %w", ctx.Err())
		}
	}
}

func (c *Channel) getUpdates(ctx context.Context, offset int64) ([]update, error) {
	ctx, cancel := context.WithTimeout(ctx, pollSeconds*time.Second+pollMargin)
	defer cancel()
	var updates []update
	err := c.do(ctx, "getUpdates", getUpdatesRequest{
		Offset:         offset,
		Limit:          1,
		Timeout:        pollSeconds,
		AllowedUpdates: []string{"message"},
	}, &updates)
	return updates, err
}

func (c *Channel) do(ctx context.Context, method string, payload, result any) error {
	raw, err := c.call(ctx, method, payload)
	if err != nil {
		return err
	}
	if result == nil {
		return nil
	}
	if err := json.Unmarshal(raw, result); err != nil {
		return fmt.Errorf("telegram %s: decode result: %w", method, err)
	}
	return nil
}

// call makes one Bot API request and returns the raw result. Its errors are safe to log.
func (c *Channel) call(ctx context.Context, method string, payload any) (json.RawMessage, error) {
	body, err := json.Marshal(payload)
	if err != nil {
		return nil, fmt.Errorf("telegram %s: encode request: %w", method, err)
	}
	endpoint := c.baseURL + "/bot" + c.token + "/" + method
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("telegram %s: build request: %w", method, withoutURL(err))
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("telegram %s: %w", method, withoutURL(err))
	}
	defer resp.Body.Close()

	var wire apiResponse
	decodeErr := json.NewDecoder(resp.Body).Decode(&wire)
	if resp.StatusCode != http.StatusOK || !wire.OK {
		return nil, &apiError{Method: method, Code: resp.StatusCode, Description: wire.Description}
	}
	if decodeErr != nil {
		return nil, fmt.Errorf("telegram %s: decode response: %w", method, decodeErr)
	}
	return wire.Result, nil
}

// withoutURL drops the request URL, which holds the bot token, from an HTTP client error.
func withoutURL(err error) error {
	var urlErr *url.Error
	if errors.As(err, &urlErr) {
		return urlErr.Err
	}
	return err
}

// apiError is a refusal from the Bot API.
type apiError struct {
	Method      string
	Code        int
	Description string
}

func (e *apiError) Error() string {
	return fmt.Sprintf("telegram %s: status %d: %s", e.Method, e.Code, e.Description)
}

// isPermanent reports a refusal that retrying cannot fix, such as a user who blocked the bot.
func isPermanent(err error) bool {
	var refused *apiError
	return errors.As(err, &refused) && refused.Code >= http.StatusBadRequest &&
		refused.Code < http.StatusInternalServerError && refused.Code != http.StatusTooManyRequests
}

// backoff returns the delay after the given number of consecutive failures.
func (c *Channel) backoff(failures int) time.Duration {
	delay := c.minBackoff
	for range failures - 1 {
		delay *= 2
		if delay >= c.maxBackoff {
			return c.maxBackoff
		}
	}
	return min(delay, c.maxBackoff)
}

// sleep waits for d and reports whether it did so without ctx being cancelled.
func sleep(ctx context.Context, d time.Duration) bool {
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-timer.C:
		return true
	}
}

type apiResponse struct {
	OK          bool            `json:"ok"`
	Description string          `json:"description"`
	Result      json.RawMessage `json:"result"`
}

type getUpdatesRequest struct {
	Offset         int64    `json:"offset"`
	Limit          int      `json:"limit"`
	Timeout        int      `json:"timeout"`
	AllowedUpdates []string `json:"allowed_updates"`
}

type sendMessageRequest struct {
	ChatID int64  `json:"chat_id"`
	Text   string `json:"text"`
}

type update struct {
	UpdateID int64    `json:"update_id"`
	Message  *message `json:"message,omitempty"`
}

type message struct {
	Text string  `json:"text,omitempty"`
	From *sender `json:"from,omitempty"`
	Chat chat    `json:"chat"`
}

type sender struct {
	ID int64 `json:"id"`
}

type chat struct {
	ID int64 `json:"id"`
}
