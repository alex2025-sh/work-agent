// Package ai implements the configuration assistant: a chat loop in which the
// model proposes TaskConfig / Workflow changes through tools that Bywork
// executes itself. The model never writes a draft and never publishes; a
// person accepts a proposal in the UI.
package ai

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// Message is an OpenAI Chat Completions message.
type Message struct {
	Role       string     `json:"role"`
	Content    string     `json:"content"`
	ToolCalls  []ToolCall `json:"tool_calls,omitempty"`
	ToolCallID string     `json:"tool_call_id,omitempty"`
	Name       string     `json:"name,omitempty"`
}

type ToolCall struct {
	ID       string       `json:"id"`
	Type     string       `json:"type"`
	Function FunctionCall `json:"function"`
}

type FunctionCall struct {
	Name      string `json:"name"`
	Arguments string `json:"arguments"`
}

// ToolDef is a client-side function tool.
type ToolDef struct {
	Type     string      `json:"type"`
	Function FunctionDef `json:"function"`
}

type FunctionDef struct {
	Name        string          `json:"name"`
	Description string          `json:"description"`
	Parameters  json.RawMessage `json:"parameters"`
}

type ChatRequest struct {
	Messages []Message
	Tools    []ToolDef
	// User keys a server-side conversation (OpenClaw derives a stable session
	// from it). Empty = stateless: the full history is sent on every call.
	User string
}

// Completion is one model response.
type Completion struct {
	Message      Message
	FinishReason string
}

// LLM is the model endpoint. OpenAICompat talks to OpenClaw (or LiteLLM, or
// any Chat Completions endpoint); Scripted is a deterministic stand-in for
// local demos and tests.
type LLM interface {
	Chat(ctx context.Context, req ChatRequest, onDelta func(string)) (*Completion, error)
	Name() string
}

// OpenAICompat calls POST {BaseURL}/chat/completions with stream=true.
type OpenAICompat struct {
	BaseURL string            // e.g. http://127.0.0.1:18789/v1
	APIKey  string            // OpenClaw gateway token / LiteLLM key
	Model   string            // OpenClaw: openclaw/<agentId>
	Headers map[string]string // extra headers (e.g. x-openclaw-model)
	Label   string
	Client  *http.Client
}

func (o *OpenAICompat) Name() string {
	if o.Label != "" {
		return o.Label
	}
	return "openai-compatible"
}

type chatBody struct {
	Model    string    `json:"model"`
	Messages []Message `json:"messages"`
	Tools    []ToolDef `json:"tools,omitempty"`
	Stream   bool      `json:"stream"`
	User     string    `json:"user,omitempty"`
}

type streamChunk struct {
	Choices []struct {
		Delta struct {
			Role      string `json:"role"`
			Content   string `json:"content"`
			ToolCalls []struct {
				Index    int    `json:"index"`
				ID       string `json:"id"`
				Type     string `json:"type"`
				Function struct {
					Name      string `json:"name"`
					Arguments string `json:"arguments"`
				} `json:"function"`
			} `json:"tool_calls"`
		} `json:"delta"`
		FinishReason *string `json:"finish_reason"`
	} `json:"choices"`
	Error *struct {
		Message string `json:"message"`
		Type    string `json:"type"`
	} `json:"error"`
}

func (o *OpenAICompat) Chat(ctx context.Context, req ChatRequest, onDelta func(string)) (*Completion, error) {
	body, err := json.Marshal(chatBody{Model: o.Model, Messages: req.Messages, Tools: req.Tools, Stream: true, User: req.User})
	if err != nil {
		return nil, err
	}
	hreq, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(o.BaseURL, "/")+"/chat/completions", bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	hreq.Header.Set("Content-Type", "application/json")
	hreq.Header.Set("Accept", "text/event-stream")
	if o.APIKey != "" {
		hreq.Header.Set("Authorization", "Bearer "+o.APIKey)
	}
	for k, v := range o.Headers {
		hreq.Header.Set(k, v)
	}
	cli := o.Client
	if cli == nil {
		cli = &http.Client{Timeout: 10 * time.Minute}
	}
	resp, err := cli.Do(hreq)
	if err != nil {
		return nil, fmt.Errorf("调用模型服务失败: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 2048))
		return nil, fmt.Errorf("模型服务返回 %d: %s", resp.StatusCode, strings.TrimSpace(string(b)))
	}
	return readStream(resp.Body, onDelta)
}

// readStream accumulates an SSE Chat Completions stream: text deltas are
// forwarded as they arrive; tool-call fragments are merged by index.
func readStream(r io.Reader, onDelta func(string)) (*Completion, error) {
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 64*1024), 8*1024*1024)
	out := &Completion{Message: Message{Role: "assistant"}}
	var text strings.Builder
	calls := map[int]*ToolCall{}
	order := []int{}
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if !strings.HasPrefix(line, "data:") {
			continue
		}
		data := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
		if data == "[DONE]" {
			break
		}
		var ch streamChunk
		if err := json.Unmarshal([]byte(data), &ch); err != nil {
			return nil, fmt.Errorf("无法解析模型流: %w", err)
		}
		if ch.Error != nil {
			return nil, fmt.Errorf("模型运行失败: %s", ch.Error.Message)
		}
		for _, c := range ch.Choices {
			if c.Delta.Content != "" {
				text.WriteString(c.Delta.Content)
				if onDelta != nil {
					onDelta(c.Delta.Content)
				}
			}
			for _, tc := range c.Delta.ToolCalls {
				cur, ok := calls[tc.Index]
				if !ok {
					cur = &ToolCall{Type: "function"}
					calls[tc.Index] = cur
					order = append(order, tc.Index)
				}
				if tc.ID != "" {
					cur.ID = tc.ID
				}
				if tc.Function.Name != "" {
					cur.Function.Name += tc.Function.Name
				}
				cur.Function.Arguments += tc.Function.Arguments
			}
			if c.FinishReason != nil {
				out.FinishReason = *c.FinishReason
			}
		}
	}
	if err := sc.Err(); err != nil {
		return nil, err
	}
	out.Message.Content = text.String()
	for i, idx := range order {
		c := calls[idx]
		if c.ID == "" {
			c.ID = fmt.Sprintf("call_%d", i)
		}
		out.Message.ToolCalls = append(out.Message.ToolCalls, *c)
	}
	if out.FinishReason == "" && len(out.Message.ToolCalls) > 0 {
		out.FinishReason = "tool_calls"
	}
	return out, nil
}
