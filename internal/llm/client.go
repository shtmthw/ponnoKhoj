// Package llm is a minimal OpenAI-compatible Chat Completions client with
// tool calling. One client works with local Ollama (/v1) and hosted
// providers, configured by LLM_BASE_URL, LLM_MODEL and LLM_API_KEY.
package llm

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// Message is one chat message.
type Message struct {
	Role       string // system | user | assistant | tool
	Content    string
	ToolCalls  []ToolCall // assistant messages only
	ToolCallID string     // tool messages only
}

// ToolCall is the model asking for a tool to be run.
type ToolCall struct {
	ID        string
	Name      string
	Arguments string // JSON object text, as produced by the model
}

// Tool describes a callable tool.
type Tool struct {
	Name        string
	Description string
	Parameters  json.RawMessage // JSON Schema of the arguments
}

// Request is one chat completion request.
type Request struct {
	Model     string
	Messages  []Message
	Tools     []Tool
	MaxTokens int
}

// Usage is the token accounting of one call.
type Usage struct {
	PromptTokens     int
	CompletionTokens int
}

// Response is the model's reply.
type Response struct {
	Message Message
	Usage   Usage
}

// Client is the interface the estimation agent depends on.
type Client interface {
	Chat(ctx context.Context, req Request) (Response, error)
}

// ErrEmptyResponse means the endpoint returned no choices.
var ErrEmptyResponse = errors.New("llm: response contains no choices")

// APIError is a non-2xx reply from the endpoint.
type APIError struct {
	Status int
	Body   string
}

func (e *APIError) Error() string { return fmt.Sprintf("llm: HTTP %d: %s", e.Status, e.Body) }

// HTTPClient talks to an OpenAI-compatible endpoint.
type HTTPClient struct {
	baseURL string
	apiKey  string
	http    *http.Client
}

// NewHTTPClient builds a client for baseURL (e.g. http://localhost:11434/v1).
func NewHTTPClient(baseURL, apiKey string) *HTTPClient {
	return &HTTPClient{
		baseURL: strings.TrimRight(baseURL, "/"),
		apiKey:  apiKey,
		http:    &http.Client{Timeout: 2 * time.Minute}, // callers pass a tighter context deadline
	}
}

// Wire types of the Chat Completions API.
type (
	wireFunctionCall struct {
		Name      string `json:"name"`
		Arguments string `json:"arguments"`
	}
	wireToolCall struct {
		ID       string           `json:"id"`
		Type     string           `json:"type"`
		Function wireFunctionCall `json:"function"`
	}
	wireMessage struct {
		Role       string         `json:"role"`
		Content    *string        `json:"content"`
		ToolCalls  []wireToolCall `json:"tool_calls,omitempty"`
		ToolCallID string         `json:"tool_call_id,omitempty"`
	}
	wireFunction struct {
		Name        string          `json:"name"`
		Description string          `json:"description"`
		Parameters  json.RawMessage `json:"parameters"`
	}
	wireTool struct {
		Type     string       `json:"type"`
		Function wireFunction `json:"function"`
	}
	wireRequest struct {
		Model       string        `json:"model"`
		Messages    []wireMessage `json:"messages"`
		Tools       []wireTool    `json:"tools,omitempty"`
		MaxTokens   int           `json:"max_tokens,omitempty"`
		Temperature float64       `json:"temperature"`
	}
	wireResponse struct {
		Choices []struct {
			Message wireMessage `json:"message"`
		} `json:"choices"`
		Usage struct {
			PromptTokens     int `json:"prompt_tokens"`
			CompletionTokens int `json:"completion_tokens"`
		} `json:"usage"`
	}
)

// Chat implements Client.
func (c *HTTPClient) Chat(ctx context.Context, req Request) (Response, error) {
	wr := wireRequest{Model: req.Model, MaxTokens: req.MaxTokens, Temperature: 0}
	for _, m := range req.Messages {
		content := m.Content
		wm := wireMessage{Role: m.Role, Content: &content, ToolCallID: m.ToolCallID}
		for _, tc := range m.ToolCalls {
			wm.ToolCalls = append(wm.ToolCalls, wireToolCall{ID: tc.ID, Type: "function", Function: wireFunctionCall{Name: tc.Name, Arguments: tc.Arguments}})
		}
		wr.Messages = append(wr.Messages, wm)
	}
	for _, t := range req.Tools {
		wr.Tools = append(wr.Tools, wireTool{Type: "function", Function: wireFunction(t)})
	}
	payload, err := json.Marshal(wr)
	if err != nil {
		return Response{}, fmt.Errorf("llm: encode request: %w", err)
	}
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+"/chat/completions", bytes.NewReader(payload))
	if err != nil {
		return Response{}, fmt.Errorf("llm: build request: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")
	if c.apiKey != "" {
		httpReq.Header.Set("Authorization", "Bearer "+c.apiKey)
	}
	resp, err := c.http.Do(httpReq)
	if err != nil {
		return Response{}, fmt.Errorf("llm: request: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil {
		return Response{}, fmt.Errorf("llm: read response: %w", err)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		snippet := string(body)
		if len(snippet) > 300 {
			snippet = snippet[:300]
		}
		return Response{}, &APIError{Status: resp.StatusCode, Body: snippet}
	}
	var wresp wireResponse
	if err := json.Unmarshal(body, &wresp); err != nil {
		return Response{}, fmt.Errorf("llm: decode response: %w", err)
	}
	if len(wresp.Choices) == 0 {
		return Response{}, ErrEmptyResponse
	}
	wm := wresp.Choices[0].Message
	out := Response{
		Message: Message{Role: "assistant"},
		Usage:   Usage{PromptTokens: wresp.Usage.PromptTokens, CompletionTokens: wresp.Usage.CompletionTokens},
	}
	if wm.Content != nil {
		out.Message.Content = *wm.Content
	}
	for _, tc := range wm.ToolCalls {
		out.Message.ToolCalls = append(out.Message.ToolCalls, ToolCall{ID: tc.ID, Name: tc.Function.Name, Arguments: tc.Function.Arguments})
	}
	return out, nil
}
