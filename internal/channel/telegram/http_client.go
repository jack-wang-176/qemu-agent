package telegram

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"
)

type HTTPClient struct {
	token   string
	client  *http.Client
	baseURL string
}

func NewHTTPClient(token string, client *http.Client) (*HTTPClient, error) {
	if strings.TrimSpace(token) == "" {
		return nil, errors.New("telegram token is empty")
	}
	if client == nil {
		client = &http.Client{Timeout: 60 * time.Second}
	}
	return &HTTPClient{token: token, client: client, baseURL: "https://api.telegram.org"}, nil
}

type apiResponse[T any] struct {
	OK          bool   `json:"ok"`
	Result      T      `json:"result"`
	ErrorCode   int    `json:"error_code"`
	Description string `json:"description"`
	Parameters  struct {
		RetryAfter int `json:"retry_after"`
	} `json:"parameters"`
}
type apiUpdate struct {
	UpdateID int64 `json:"update_id"`
	Message  *struct {
		MessageID       int64  `json:"message_id"`
		MessageThreadID int64  `json:"message_thread_id"`
		Text            string `json:"text"`
		From            *struct {
			ID       int64  `json:"id"`
			IsBot    bool   `json:"is_bot"`
			Username string `json:"username"`
		} `json:"from"`
		Chat struct {
			ID   int64  `json:"id"`
			Type string `json:"type"`
		} `json:"chat"`
	} `json:"message"`
}
type apiMessage struct {
	MessageID int64  `json:"message_id"`
	Text      string `json:"text"`
	Chat      struct {
		ID int64 `json:"id"`
	} `json:"chat"`
}

func (c *HTTPClient) Poll(ctx context.Context, req PollRequest) (PollResult, error) {
	var raw []apiUpdate
	if err := c.call(ctx, "getUpdates", map[string]any{"offset": req.Offset, "timeout": int(req.Timeout.Seconds()), "limit": req.Limit, "allowed_updates": []string{"message"}}, &raw); err != nil {
		return PollResult{}, err
	}
	result := PollResult{Updates: make([]Update, 0, len(raw)), NextOffset: req.Offset}
	for _, item := range raw {
		u := Update{ID: item.UpdateID}
		if item.Message != nil {
			m := item.Message
			u.Message = &Message{ID: m.MessageID, Text: m.Text, ThreadID: m.MessageThreadID, Chat: Chat{ID: m.Chat.ID, Type: m.Chat.Type}}
			if m.From != nil {
				u.Message.From = &User{ID: m.From.ID, IsBot: m.From.IsBot, Username: m.From.Username}
			}
		}
		result.Updates = append(result.Updates, u)
		if item.UpdateID >= result.NextOffset {
			result.NextOffset = item.UpdateID + 1
		}
	}
	return result, nil
}
func (c *HTTPClient) SendMessage(ctx context.Context, req SendRequest) (SentMessage, error) {
	var raw apiMessage
	body := map[string]any{"chat_id": req.Target.ChatID, "text": req.Text}
	if req.Target.ThreadID != 0 {
		body["message_thread_id"] = req.Target.ThreadID
	}
	if req.Target.ReplyTo != 0 {
		body["reply_parameters"] = map[string]any{"message_id": req.Target.ReplyTo}
	}
	if err := c.call(ctx, "sendMessage", body, &raw); err != nil {
		return SentMessage{}, err
	}
	return SentMessage{ID: raw.MessageID, ChatID: raw.Chat.ID, Text: raw.Text}, nil
}
func (c *HTTPClient) EditMessage(ctx context.Context, req EditRequest) (SentMessage, error) {
	var raw apiMessage
	if err := c.call(ctx, "editMessageText", map[string]any{"chat_id": req.ChatID, "message_id": req.MessageID, "text": req.Text}, &raw); err != nil {
		return SentMessage{}, err
	}
	return SentMessage{ID: raw.MessageID, ChatID: raw.Chat.ID, Text: raw.Text}, nil
}

func (c *HTTPClient) call(ctx context.Context, method string, body any, out any) error {
	data, err := json.Marshal(body)
	if err != nil {
		return err
	}
	url := c.baseURL + "/bot" + c.token + "/" + method
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(data))
	if err != nil {
		return err
	}
	httpReq.Header.Set("Content-Type", "application/json")
	resp, err := c.client.Do(httpReq)
	if err != nil {
		return &APIError{Kind: ErrorTransient, Operation: method, Err: err}
	}
	defer resp.Body.Close()
	payload, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return &APIError{Kind: ErrorTransient, Operation: method, StatusCode: resp.StatusCode, Err: err}
	}
	var envelope json.RawMessage
	var base apiResponse[json.RawMessage]
	if err := json.Unmarshal(payload, &base); err != nil {
		return &APIError{Kind: ErrorProtocol, Operation: method, StatusCode: resp.StatusCode, Err: err}
	}
	envelope = base.Result
	if !base.OK {
		statusCode := resp.StatusCode
		if base.ErrorCode != 0 {
			statusCode = base.ErrorCode
		}
		kind := ErrorBadRequest
		if statusCode == 429 {
			kind = ErrorRateLimited
		}
		if statusCode >= 500 {
			kind = ErrorTransient
		}
		if statusCode == 401 || statusCode == 404 {
			kind = ErrorAuthentication
		}
		return &APIError{Kind: kind, Operation: method, StatusCode: statusCode, RetryAfter: time.Duration(base.Parameters.RetryAfter) * time.Second, Err: errors.New(strconv.Itoa(base.ErrorCode))}
	}
	if err := json.Unmarshal(envelope, out); err != nil {
		return &APIError{Kind: ErrorProtocol, Operation: method, StatusCode: resp.StatusCode, Err: fmt.Errorf("decode result: %w", err)}
	}
	return nil
}
