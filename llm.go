package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// Message is a single chat-completions message.
type Message struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

// ToolCall is the structured output we extract from the model's reply.
type ToolCall struct {
	Tool string          `json:"tool"`
	Args json.RawMessage `json:"args"`
}

// LLM is a thin client for MiniMax cloud chat-completions.
type LLM struct {
	key, groupID, model, url string
	http                     *http.Client
}

// NewLLM constructs a client. groupID may be empty.
func NewLLM(key, groupID, model string) *LLM {
	return &LLM{
		key:     key,
		groupID: groupID,
		model:   model,
		url:     "https://api.minimax.io/v1/text/chatcompletion_v2",
		http:    &http.Client{Timeout: 120 * time.Second},
	}
}

type chatReq struct {
	Model    string    `json:"model"`
	Messages []Message `json:"messages"`
}

type chatResp struct {
	Choices []struct {
		Message Message `json:"message"`
	} `json:"choices"`
	BaseResp *struct {
		StatusCode int    `json:"status_code"`
		StatusMsg  string `json:"status_msg"`
	} `json:"base_resp,omitempty"`
}

// Chat sends messages and returns the assistant's content.
func (l *LLM) Chat(ctx context.Context, msgs []Message) (string, error) {
	body, err := json.Marshal(chatReq{Model: l.model, Messages: msgs})
	if err != nil {
		return "", err
	}
	url := l.url
	if l.groupID != "" {
		url += "?GroupId=" + l.groupID
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return "", err
	}
	req.Header.Set("Authorization", "Bearer "+l.key)
	req.Header.Set("Content-Type", "application/json")

	resp, err := l.http.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	if resp.StatusCode >= 300 {
		return "", fmt.Errorf("minimax http %d: %s", resp.StatusCode, string(raw))
	}
	var out chatResp
	if err := json.Unmarshal(raw, &out); err != nil {
		return "", fmt.Errorf("decode: %w (body=%s)", err, string(raw))
	}
	if out.BaseResp != nil && out.BaseResp.StatusCode != 0 {
		return "", fmt.Errorf("minimax api %d: %s", out.BaseResp.StatusCode, out.BaseResp.StatusMsg)
	}
	if len(out.Choices) == 0 {
		return "", fmt.Errorf("minimax: empty choices (body=%s)", string(raw))
	}
	return out.Choices[0].Message.Content, nil
}

// ChatTool calls Chat and extracts a single ToolCall from the reply, retrying
// up to 3 times with an explicit reprompt on JSON parse failure.
func (l *LLM) ChatTool(ctx context.Context, msgs []Message) (ToolCall, string, error) {
	conv := append([]Message{}, msgs...)
	var lastRaw string
	for attempt := 0; attempt < 3; attempt++ {
		reply, err := l.Chat(ctx, conv)
		if err != nil {
			return ToolCall{}, "", err
		}
		lastRaw = reply
		call, perr := parseToolCall(reply)
		if perr == nil {
			return call, reply, nil
		}
		conv = append(conv,
			Message{Role: "assistant", Content: reply},
			Message{Role: "user", Content: "Your last reply was not a valid tool call: " + perr.Error() +
				". Reply with EXACTLY one JSON object {\"tool\":\"...\",\"args\":{...}} and nothing else."},
		)
	}
	return ToolCall{}, lastRaw, fmt.Errorf("tool-call parse failed after 3 attempts; last raw=%q", lastRaw)
}

// parseToolCall finds the first top-level JSON object in s and unmarshals it.
func parseToolCall(s string) (ToolCall, error) {
	s = strings.TrimSpace(s)
	// Strip a leading ```json / ``` fence if present.
	if strings.HasPrefix(s, "```") {
		if i := strings.Index(s, "\n"); i >= 0 {
			s = s[i+1:]
		}
		s = strings.TrimSuffix(s, "```")
		s = strings.TrimSpace(s)
	}
	start := strings.Index(s, "{")
	end := strings.LastIndex(s, "}")
	if start < 0 || end < start {
		return ToolCall{}, fmt.Errorf("no JSON object found")
	}
	var call ToolCall
	if err := json.Unmarshal([]byte(s[start:end+1]), &call); err != nil {
		return ToolCall{}, err
	}
	if call.Tool == "" {
		return ToolCall{}, fmt.Errorf("missing \"tool\" field")
	}
	if len(call.Args) == 0 {
		call.Args = json.RawMessage("{}")
	}
	return call, nil
}
