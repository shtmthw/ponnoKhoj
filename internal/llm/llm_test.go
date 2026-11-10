package llm

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestHTTPClientChat(t *testing.T) {
	t.Parallel()
	var got map[string]any
	var auth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/chat/completions" || r.Method != http.MethodPost {
			http.NotFound(w, r)
			return
		}
		auth = r.Header.Get("Authorization")
		body, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(body, &got)
		_, _ = io.WriteString(w, `{"choices":[{"message":{"role":"assistant","content":null,"tool_calls":[{"id":"call_9","type":"function","function":{"name":"get_local_listings","arguments":"{}"}}]}}],"usage":{"prompt_tokens":123,"completion_tokens":7}}`)
	}))
	defer srv.Close()

	c := NewHTTPClient(srv.URL+"/v1/", "sk-test")
	resp, err := c.Chat(context.Background(), Request{
		Model: "m1", MaxTokens: 256,
		Messages: []Message{
			{Role: "system", Content: "rules"},
			{Role: "assistant", ToolCalls: []ToolCall{{ID: "call_1", Name: "get_area_info", Arguments: "{}"}}},
			{Role: "tool", ToolCallID: "call_1", Content: `{"district_en":"Dhaka"}`},
		},
		Tools: []Tool{{Name: "get_local_listings", Description: "d", Parameters: json.RawMessage(`{"type":"object"}`)}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if auth != "Bearer sk-test" {
		t.Errorf("Authorization = %q", auth)
	}
	if got["model"] != "m1" || got["max_tokens"] != float64(256) || got["temperature"] != float64(0) {
		t.Errorf("request body = %v", got)
	}
	msgs := got["messages"].([]any)
	if len(msgs) != 3 || msgs[2].(map[string]any)["tool_call_id"] != "call_1" {
		t.Errorf("messages = %v", msgs)
	}
	call := msgs[1].(map[string]any)["tool_calls"].([]any)[0].(map[string]any)
	if call["type"] != "function" || call["function"].(map[string]any)["name"] != "get_area_info" {
		t.Errorf("tool call encoding = %v", call)
	}
	tool := got["tools"].([]any)[0].(map[string]any)
	if tool["type"] != "function" || tool["function"].(map[string]any)["name"] != "get_local_listings" {
		t.Errorf("tool encoding = %v", tool)
	}
	if resp.Message.Role != "assistant" || resp.Message.Content != "" || len(resp.Message.ToolCalls) != 1 || resp.Message.ToolCalls[0].Name != "get_local_listings" || resp.Message.ToolCalls[0].ID != "call_9" {
		t.Errorf("response = %+v", resp)
	}
	if resp.Usage != (Usage{PromptTokens: 123, CompletionTokens: 7}) {
		t.Errorf("usage = %+v", resp.Usage)
	}
}

func TestHTTPClientErrors(t *testing.T) {
	t.Parallel()
	var mode string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "" {
			t.Error("Authorization sent without an API key")
		}
		switch mode {
		case "500":
			http.Error(w, strings.Repeat("upstream exploded ", 100), http.StatusInternalServerError)
		case "garbage":
			_, _ = io.WriteString(w, "<html>not json</html>")
		case "empty":
			_, _ = io.WriteString(w, `{"choices":[]}`)
		default:
			_, _ = io.WriteString(w, `{"choices":[{"message":{"role":"assistant","content":"hello"}}]}`)
		}
	}))
	defer srv.Close()
	c := NewHTTPClient(srv.URL, "")
	req := Request{Model: "m", Messages: []Message{{Role: "user", Content: "hi"}}}

	if resp, err := c.Chat(context.Background(), req); err != nil || resp.Message.Content != "hello" {
		t.Errorf("plain answer: %+v, %v", resp, err)
	}
	mode = "500"
	_, err := c.Chat(context.Background(), req)
	var apiErr *APIError
	if !errors.As(err, &apiErr) || apiErr.Status != 500 || len(apiErr.Body) > 300 || !strings.Contains(apiErr.Error(), "HTTP 500") {
		t.Errorf("500 err = %v", err)
	}
	mode = "garbage"
	if _, err := c.Chat(context.Background(), req); err == nil || !strings.Contains(err.Error(), "decode") {
		t.Errorf("garbage err = %v", err)
	}
	mode = "empty"
	if _, err := c.Chat(context.Background(), req); !errors.Is(err, ErrEmptyResponse) {
		t.Errorf("empty err = %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := c.Chat(ctx, req); !errors.Is(err, context.Canceled) {
		t.Errorf("cancelled err = %v", err)
	}
	dead := NewHTTPClient("http://127.0.0.1:1", "")
	if _, err := dead.Chat(context.Background(), req); err == nil {
		t.Error("unreachable endpoint returned no error")
	}
}

func TestFakeClient(t *testing.T) {
	t.Parallel()
	f := NewFakeClient(Response{Message: Message{Content: "one"}}, Response{Message: Message{Content: "two"}})
	for _, want := range []string{"one", "two"} {
		resp, err := f.Chat(context.Background(), Request{Model: "m"})
		if err != nil || resp.Message.Content != want {
			t.Errorf("got %q, %v; want %q", resp.Message.Content, err, want)
		}
	}
	if _, err := f.Chat(context.Background(), Request{}); !errors.Is(err, ErrScriptExhausted) {
		t.Errorf("exhausted err = %v", err)
	}
	if f.Calls() != 3 || f.Requests[0].Model != "m" {
		t.Errorf("recorded %d calls", f.Calls())
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := f.Chat(ctx, Request{}); !errors.Is(err, context.Canceled) {
		t.Errorf("cancelled err = %v", err)
	}

	rec, err := LoadRecording("../../test/fixtures/llm/estimate_ok.json")
	if err != nil {
		t.Fatal(err)
	}
	first, _ := rec.Chat(context.Background(), Request{})
	if len(first.Message.ToolCalls) != 2 || first.Message.ToolCalls[0].Name != "get_local_listings" || first.Message.ToolCalls[0].Arguments != "{}" || first.Usage.PromptTokens != 410 {
		t.Errorf("recording step 1 = %+v", first)
	}
	if _, err := LoadRecording("missing.json"); err == nil {
		t.Error("missing recording accepted")
	}
	if _, err := LoadRecording("../../go.mod"); err == nil {
		t.Error("non-JSON recording accepted")
	}
}
