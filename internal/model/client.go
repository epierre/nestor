package model

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net/http"
	"strconv"
	"strings"
)

const (
	chatCompletionsPath = "/chat/completions"
	costHeader          = "x-litellm-response-cost"
	toolTypeFunction    = "function"
)

var _ ModelClient = (*Client)(nil)

// StatusError reports a response with a status other than 200. It leaves out the response body
// because a proxy can echo credentials, and this error reaches the logs and the call log.
type StatusError struct {
	Code int
}

func (e *StatusError) Error() string {
	return fmt.Sprintf("model endpoint returned status %d", e.Code)
}

// Client calls an OpenAI-compatible chat completions endpoint.
type Client struct {
	url    string
	apiKey string
	http   *http.Client
}

// NewClient returns a Client for the endpoint rooted at baseURL, such as http://litellm:4000/v1.
func NewClient(baseURL, apiKey string) *Client {
	return &Client{
		url:    strings.TrimRight(baseURL, "/") + chatCompletionsPath,
		apiKey: apiKey,
		http:   &http.Client{},
	}
}

// Complete sends req and returns the first choice of the response.
func (c *Client) Complete(ctx context.Context, req CompletionRequest) (CompletionResponse, error) {
	body, err := json.Marshal(toWireRequest(req))
	if err != nil {
		return CompletionResponse{}, fmt.Errorf("encode request: %w", err)
	}
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, c.url, bytes.NewReader(body))
	if err != nil {
		return CompletionResponse{}, fmt.Errorf("build request: %w", err)
	}
	httpReq.Header.Set("Authorization", "Bearer "+c.apiKey)
	httpReq.Header.Set("Content-Type", "application/json")

	httpResp, err := c.http.Do(httpReq)
	if err != nil {
		return CompletionResponse{}, fmt.Errorf("send request: %w", err)
	}
	defer httpResp.Body.Close()

	if httpResp.StatusCode != http.StatusOK {
		return CompletionResponse{}, &StatusError{Code: httpResp.StatusCode}
	}
	var wire wireResponse
	if err := json.NewDecoder(httpResp.Body).Decode(&wire); err != nil {
		return CompletionResponse{}, fmt.Errorf("decode response: %w", err)
	}
	if len(wire.Choices) == 0 {
		return CompletionResponse{}, errors.New("model response has no choices")
	}
	return fromWireResponse(wire, httpResp.Header.Get(costHeader)), nil
}

// The wire types follow the OpenAI chat completions format. Tool call arguments travel as a
// string holding JSON, and an assistant message that only calls tools has null content.

type wireRequest struct {
	Model     string        `json:"model"`
	Messages  []wireMessage `json:"messages"`
	Tools     []wireTool    `json:"tools,omitempty"`
	MaxTokens int           `json:"max_tokens,omitempty"`
}

type wireMessage struct {
	Role       Role           `json:"role"`
	Content    *string        `json:"content"`
	ToolCalls  []wireToolCall `json:"tool_calls,omitempty"`
	ToolCallID string         `json:"tool_call_id,omitempty"`
}

type wireTool struct {
	Type     string       `json:"type"`
	Function wireFunction `json:"function"`
}

type wireFunction struct {
	Name        string          `json:"name"`
	Description string          `json:"description,omitempty"`
	Parameters  json.RawMessage `json:"parameters,omitempty"`
}

type wireToolCall struct {
	ID       string `json:"id"`
	Type     string `json:"type"`
	Function struct {
		Name      string `json:"name"`
		Arguments string `json:"arguments"`
	} `json:"function"`
}

type wireResponse struct {
	Choices []struct {
		Message struct {
			Content   string         `json:"content"`
			ToolCalls []wireToolCall `json:"tool_calls"`
		} `json:"message"`
	} `json:"choices"`
	Usage struct {
		PromptTokens     int      `json:"prompt_tokens"`
		CompletionTokens int      `json:"completion_tokens"`
		Cost             *float64 `json:"cost"`
	} `json:"usage"`
}

func toWireRequest(req CompletionRequest) wireRequest {
	wire := wireRequest{Model: req.Model, MaxTokens: req.MaxTokens}
	for _, m := range req.Messages {
		wire.Messages = append(wire.Messages, toWireMessage(m))
	}
	for _, t := range req.Tools {
		wire.Tools = append(wire.Tools, wireTool{
			Type: toolTypeFunction,
			Function: wireFunction{
				Name:        t.Name,
				Description: t.Description,
				Parameters:  t.Parameters,
			},
		})
	}
	return wire
}

func toWireMessage(m Message) wireMessage {
	wire := wireMessage{Role: m.Role, ToolCallID: m.ToolCallID}
	if m.Content != "" || len(m.ToolCalls) == 0 {
		wire.Content = &m.Content
	}
	for _, call := range m.ToolCalls {
		wireCall := wireToolCall{ID: call.ID, Type: toolTypeFunction}
		wireCall.Function.Name = call.Name
		wireCall.Function.Arguments = string(call.Arguments)
		wire.ToolCalls = append(wire.ToolCalls, wireCall)
	}
	return wire
}

func fromWireResponse(wire wireResponse, costValue string) CompletionResponse {
	message := wire.Choices[0].Message
	resp := CompletionResponse{
		Content: message.Content,
		Usage: Usage{
			PromptTokens:     wire.Usage.PromptTokens,
			CompletionTokens: wire.Usage.CompletionTokens,
			CostUSD:          reportedCost(costValue, wire.Usage.Cost),
		},
	}
	for _, call := range message.ToolCalls {
		resp.ToolCalls = append(resp.ToolCalls, ToolCall{
			ID:        call.ID,
			Name:      call.Function.Name,
			Arguments: json.RawMessage(call.Function.Arguments),
		})
	}
	return resp
}

// reportedCost prefers the proxy's cost header, falls back to the usage field, and returns nil
// when neither holds a usable cost. LiteLLM can send the header with the value None.
func reportedCost(header string, field *float64) *float64 {
	if cost, err := strconv.ParseFloat(header, 64); err == nil && validCost(cost) {
		return &cost
	}
	if field != nil && validCost(*field) {
		return field
	}
	return nil
}

func validCost(cost float64) bool {
	return cost >= 0 && !math.IsInf(cost, 1)
}
