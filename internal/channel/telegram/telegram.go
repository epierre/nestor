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
	"strings"
	"time"
	"unicode/utf16"

	"github.com/epierre/nestor/internal/approval"
	"github.com/epierre/nestor/internal/channel"
	"github.com/epierre/nestor/internal/identity"
	"github.com/epierre/nestor/internal/obs"
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

	maxMessageLength = 4096 // Telegram counts UTF-16 code units

	noticeFailure  = "Something went wrong, try again shortly."
	noticeTextOnly = "I can only read text messages for now."

	cardQuestion       = "\n\nApprove this action?"
	callbackApprove    = "approve"
	callbackReject     = "reject"
	answerApproved     = "Approved."
	answerRejected     = "Rejected."
	answerNoLongerPend = "This action is no longer pending."
	answerExpired      = "This action has expired."
	answerNotVerified  = "This action could not be verified, so it did not run."
	answerFailed       = "The action failed."
)

var _ channel.Channel = (*Channel)(nil)

// Identities resolves a platform identity to the internal user behind it. It returns
// store.ErrIdentityNotFound for one that is unknown or disabled.
type Identities interface {
	ResolveIdentity(ctx context.Context, platform, externalID string) (identity.UserID, error)
}

// Decider carries out the user's decision on a pending action. approval.Gate implements it.
type Decider interface {
	Decide(ctx context.Context, user identity.UserID, actionID string, decision approval.Decision) (store.PendingAction, error)
}

// Channel long-polls Telegram for messages. The bot token is part of every request URL, so errors
// from the HTTP client must pass through call before they reach a log.
type Channel struct {
	baseURL    string
	token      string
	identities Identities
	decider    Decider
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

// SetDecider sets who receives the decisions made with approval buttons. Until it is set, a button
// press is answered as no longer pending.
func (c *Channel) SetDecider(d Decider) {
	c.decider = d
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
	if u.CallbackQuery != nil {
		return c.handleCallback(ctx, *u.CallbackQuery)
	}
	if u.Message == nil || u.Message.From == nil {
		return nil
	}
	m := u.Message
	ctx = obs.WithRequestID(ctx, obs.NewRequestID())
	user, err := c.identities.ResolveIdentity(ctx, platform, strconv.FormatInt(m.From.ID, 10))
	if errors.Is(err, store.ErrIdentityNotFound) {
		slog.WarnContext(ctx, "ignored message from unknown sender", "telegram_id", m.From.ID)
		return nil
	}
	if err != nil {
		// Nothing can be sent: the sender may be a stranger.
		return fmt.Errorf("resolve sender: %w", err)
	}

	ctx = withChat(identity.WithUser(ctx, user), m.Chat.ID)
	slog.InfoContext(ctx, "message received")

	reply := noticeTextOnly
	if m.Text != "" {
		reply, err = h.HandleMessage(ctx, m.Text)
		if err != nil {
			slog.ErrorContext(ctx, "message handling failed", "error", err)
			reply = noticeFailure
		}
	}
	err = c.Send(ctx, m.Chat.ID, reply)
	if isPermanent(err) {
		slog.ErrorContext(ctx, "reply refused", "error", err)
		return nil
	}
	if err != nil {
		return err
	}
	slog.InfoContext(ctx, "reply sent")
	return nil
}

// handleCallback routes an Approve or Reject press to the decider, for the user who pressed it.
// It returns an error only when the decision is unknown and the press must be delivered again.
func (c *Channel) handleCallback(ctx context.Context, q callbackQuery) error {
	if q.From == nil || q.Message == nil {
		return nil
	}
	ctx = obs.WithRequestID(ctx, obs.NewRequestID())
	user, err := c.identities.ResolveIdentity(ctx, platform, strconv.FormatInt(q.From.ID, 10))
	if errors.Is(err, store.ErrIdentityNotFound) {
		slog.WarnContext(ctx, "ignored button press from unknown sender", "telegram_id", q.From.ID)
		return nil
	}
	if err != nil {
		return fmt.Errorf("resolve sender: %w", err)
	}
	ctx = withChat(identity.WithUser(ctx, user), q.Message.Chat.ID)

	decision, actionID, ok := parseCallback(q.Data)
	if !ok || c.decider == nil {
		c.answer(ctx, q.ID, answerNoLongerPend)
		return nil
	}
	action, err := c.decider.Decide(ctx, user, actionID, decision)
	if !settled(action, err) {
		return fmt.Errorf("decide: %w", err)
	}
	c.answer(ctx, q.ID, answerText(decision, action, err))
	if text := outcomeMessage(action, err); text != "" {
		// The decision is final, so a failed send is logged rather than redelivered: a second
		// delivery would find the action settled and post nothing.
		if sendErr := c.Send(ctx, q.Message.Chat.ID, text); sendErr != nil {
			slog.WarnContext(ctx, "outcome not sent", "error", sendErr)
		}
	}
	return nil
}

// outcomeMessage is the chat message that reports a settled decision. It is empty when there is
// nothing to report, as for an action that was no longer pending.
func outcomeMessage(action store.PendingAction, err error) string {
	switch {
	case errors.Is(err, approval.ErrNoPendingAction):
		return ""
	case errors.Is(err, approval.ErrIntegrity):
		return "Not run, its details changed after the proposal: " + action.Preview
	case action.State == store.PendingExecuted:
		return "Done: " + action.Preview
	case action.State == store.PendingRejected:
		return "Rejected: " + action.Preview
	case action.State == store.PendingExpired:
		return "Expired, not run: " + action.Preview
	case action.State == store.PendingFailed:
		return "Failed: " + action.Preview
	}
	return ""
}

// settled reports whether a decision reached an outcome that the user can be told about, so the
// press need not be delivered again. A failed execution is settled: the tool ran and failed.
func settled(action store.PendingAction, err error) bool {
	return err == nil || action.State == store.PendingFailed ||
		errors.Is(err, approval.ErrNoPendingAction) || errors.Is(err, approval.ErrExpired) ||
		errors.Is(err, approval.ErrIntegrity)
}

// answerText is what the user sees on the button's answer popup.
func answerText(decision approval.Decision, action store.PendingAction, err error) string {
	switch {
	case errors.Is(err, approval.ErrNoPendingAction):
		return answerNoLongerPend
	case errors.Is(err, approval.ErrExpired):
		return answerExpired
	case errors.Is(err, approval.ErrIntegrity):
		return answerNotVerified
	case err != nil || action.State == store.PendingFailed:
		return answerFailed
	case decision == approval.Reject:
		return answerRejected
	default:
		return answerApproved
	}
}

// parseCallback reads the "approve:<id>" or "reject:<id>" data that a card's buttons carry.
func parseCallback(data string) (approval.Decision, string, bool) {
	verb, id, ok := strings.Cut(data, ":")
	if !ok || id == "" {
		return "", "", false
	}
	switch verb {
	case callbackApprove:
		return approval.Approve, id, true
	case callbackReject:
		return approval.Reject, id, true
	}
	return "", "", false
}

// SendApprovalCard shows a pending action in the chat carried by ctx, with Approve and Reject
// buttons. The chat is the one the action's user is talking in, so the card reaches that user
// only. It tries once: the action stays pending, and its card is re-sent later if this fails.
func (c *Channel) SendApprovalCard(ctx context.Context, action store.PendingAction) error {
	chatID, ok := chatFrom(ctx)
	if !ok {
		return errors.New("approval card: no chat in context")
	}
	text := action.Preview + cardQuestion
	if len(split(text)) != 1 {
		return fmt.Errorf("approval card: text longer than %d characters", maxMessageLength)
	}
	payload := cardRequest{
		ChatID: chatID,
		Text:   text,
		ReplyMarkup: inlineKeyboard{InlineKeyboard: [][]button{{
			{Text: "Approve", CallbackData: callbackApprove + ":" + action.ID},
			{Text: "Reject", CallbackData: callbackReject + ":" + action.ID},
		}}},
	}
	sendCtx, cancel := context.WithTimeout(ctx, sendTimeout)
	defer cancel()
	if err := c.do(sendCtx, "sendMessage", payload, nil); err != nil {
		return fmt.Errorf("send approval card: %w", err)
	}
	return nil
}

// answer dismisses the button's loading state with text. A failure is logged, since the press
// itself has already been handled.
func (c *Channel) answer(ctx context.Context, queryID, text string) {
	sendCtx, cancel := context.WithTimeout(ctx, sendTimeout)
	defer cancel()
	if err := c.do(sendCtx, "answerCallbackQuery", answerCallbackRequest{CallbackQueryID: queryID, Text: text}, nil); err != nil {
		slog.WarnContext(ctx, "callback answer not sent", "error", err)
	}
}

type chatKey struct{}

// withChat returns a copy of ctx carrying the chat that the current update came from.
func withChat(ctx context.Context, chatID int64) context.Context {
	return context.WithValue(ctx, chatKey{}, chatID)
}

func chatFrom(ctx context.Context) (int64, bool) {
	id, ok := ctx.Value(chatKey{}).(int64)
	return id, ok
}

// Send delivers text to the chat in consecutive messages within Telegram's size limit, retrying
// transient failures until ctx is cancelled. Empty text sends nothing, as Telegram refuses it.
func (c *Channel) Send(ctx context.Context, chatID int64, text string) error {
	for _, part := range split(text) {
		if err := c.sendPart(ctx, chatID, part); err != nil {
			return err
		}
	}
	return nil
}

// split cuts text into parts within maxMessageLength, never inside a character.
func split(text string) []string {
	var parts []string
	start, length := 0, 0
	for i, r := range text {
		units := utf16.RuneLen(r)
		if length+units > maxMessageLength {
			parts = append(parts, text[start:i])
			start, length = i, 0
		}
		length += units
	}
	if start < len(text) {
		parts = append(parts, text[start:])
	}
	return parts
}

func (c *Channel) sendPart(ctx context.Context, chatID int64, text string) error {
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
		AllowedUpdates: []string{"message", "callback_query"},
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
	UpdateID      int64          `json:"update_id"`
	Message       *message       `json:"message,omitempty"`
	CallbackQuery *callbackQuery `json:"callback_query,omitempty"`
}

type callbackQuery struct {
	ID      string   `json:"id"`
	From    *sender  `json:"from,omitempty"`
	Message *message `json:"message,omitempty"`
	Data    string   `json:"data"`
}

type cardRequest struct {
	ChatID      int64          `json:"chat_id"`
	Text        string         `json:"text"`
	ReplyMarkup inlineKeyboard `json:"reply_markup"`
}

type inlineKeyboard struct {
	InlineKeyboard [][]button `json:"inline_keyboard"`
}

type button struct {
	Text         string `json:"text"`
	CallbackData string `json:"callback_data"`
}

type answerCallbackRequest struct {
	CallbackQueryID string `json:"callback_query_id"`
	Text            string `json:"text"`
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
