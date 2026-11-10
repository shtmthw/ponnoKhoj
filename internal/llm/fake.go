package llm

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"sync"
)

// FakeClient is a deterministic Client for tests. It replays a recorded
// script of responses in order and records every request it receives.
type FakeClient struct {
	mu       sync.Mutex
	script   []Response
	next     int
	Requests []Request
	// Err, when set, is returned by every call instead of a response.
	Err error
}

// NewFakeClient replays the given responses in order.
func NewFakeClient(script ...Response) *FakeClient {
	return &FakeClient{script: script}
}

// ErrScriptExhausted is returned when the fake has no responses left.
var ErrScriptExhausted = errors.New("llm: fake script exhausted")

// Chat implements Client.
func (f *FakeClient) Chat(ctx context.Context, req Request) (Response, error) {
	if err := ctx.Err(); err != nil {
		return Response{}, err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.Requests = append(f.Requests, req)
	if f.Err != nil {
		return Response{}, f.Err
	}
	if f.next >= len(f.script) {
		return Response{}, ErrScriptExhausted
	}
	resp := f.script[f.next]
	f.next++
	return resp, nil
}

// Calls reports how many requests the fake has served.
func (f *FakeClient) Calls() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.Requests)
}

// recordedStep is the on-disk form of one scripted response.
type recordedStep struct {
	Content   string `json:"content,omitempty"`
	ToolCalls []struct {
		ID        string          `json:"id"`
		Name      string          `json:"name"`
		Arguments json.RawMessage `json:"arguments"`
	} `json:"tool_calls,omitempty"`
	PromptTokens     int `json:"prompt_tokens,omitempty"`
	CompletionTokens int `json:"completion_tokens,omitempty"`
}

// LoadRecording reads a JSON file holding an array of recorded responses
// (see test/fixtures/llm) and returns a FakeClient that replays it.
func LoadRecording(path string) (*FakeClient, error) {
	data, err := os.ReadFile(path) //nolint:gosec // test fixture path chosen by the caller
	if err != nil {
		return nil, fmt.Errorf("llm: read recording: %w", err)
	}
	var steps []recordedStep
	if err := json.Unmarshal(data, &steps); err != nil {
		return nil, fmt.Errorf("llm: parse recording %s: %w", path, err)
	}
	script := make([]Response, 0, len(steps))
	for _, s := range steps {
		r := Response{
			Message: Message{Role: "assistant", Content: s.Content},
			Usage:   Usage{PromptTokens: s.PromptTokens, CompletionTokens: s.CompletionTokens},
		}
		for _, tc := range s.ToolCalls {
			r.Message.ToolCalls = append(r.Message.ToolCalls, ToolCall{ID: tc.ID, Name: tc.Name, Arguments: string(tc.Arguments)})
		}
		script = append(script, r)
	}
	return NewFakeClient(script...), nil
}
