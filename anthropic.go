// Anthropic Messages API (/v1/messages) in front of the same chat-completions pipeline, so Claude Code and
// other Anthropic-format clients can use any provider. The request is translated to a chat body before the
// filter sees it, and the answer (plain or streamed) is translated back.
// ponytail: text, images, tools and tool results only. Thinking blocks, citations and prompt-cache hints are dropped.
package main

import (
	"bufio"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
)

type aBlock struct {
	Type      string          `json:"type"`
	Text      string          `json:"text"`
	ID        string          `json:"id"`
	Name      string          `json:"name"`
	Input     json.RawMessage `json:"input"`
	ToolUseID string          `json:"tool_use_id"`
	Content   json.RawMessage `json:"content"`
	Source    struct {
		Type      string `json:"type"`
		MediaType string `json:"media_type"`
		Data      string `json:"data"`
		URL       string `json:"url"`
	} `json:"source"`
}

// blocks reads a content field, which is either a string or a list of blocks.
func blocks(raw json.RawMessage) []aBlock {
	var s string
	if json.Unmarshal(raw, &s) == nil {
		return []aBlock{{Type: "text", Text: s}}
	}
	var bs []aBlock
	json.Unmarshal(raw, &bs)
	return bs
}

func joinText(bs []aBlock) string {
	var parts []string
	for _, b := range bs {
		if b.Type == "text" {
			parts = append(parts, b.Text)
		}
	}
	return strings.Join(parts, "\n")
}

// anthropicToChat turns a Messages request into a chat-completions body.
func anthropicToChat(raw []byte) ([]byte, error) {
	var in struct {
		Model    string          `json:"model"`
		System   json.RawMessage `json:"system"`
		Messages []struct {
			Role    string          `json:"role"`
			Content json.RawMessage `json:"content"`
		} `json:"messages"`
		MaxTokens   int      `json:"max_tokens"`
		Temperature *float64 `json:"temperature"`
		TopP        *float64 `json:"top_p"`
		Stop        []string `json:"stop_sequences"`
		Stream      bool     `json:"stream"`
		Tools       []struct {
			Name        string          `json:"name"`
			Description string          `json:"description"`
			InputSchema json.RawMessage `json:"input_schema"`
		} `json:"tools"`
		ToolChoice *struct {
			Type string `json:"type"`
			Name string `json:"name"`
		} `json:"tool_choice"`
	}
	if err := json.Unmarshal(raw, &in); err != nil {
		return nil, err
	}
	var msgs []map[string]any
	if s := joinText(blocks(in.System)); s != "" {
		msgs = append(msgs, map[string]any{"role": "system", "content": s})
	}
	for _, m := range in.Messages {
		bs := blocks(m.Content)
		if m.Role == "assistant" {
			am := map[string]any{"role": "assistant"}
			if s := joinText(bs); s != "" {
				am["content"] = s
			} else {
				am["content"] = nil
			}
			var calls []map[string]any
			for _, b := range bs {
				if b.Type == "tool_use" {
					args := string(b.Input)
					if args == "" || args == "null" {
						args = "{}"
					}
					calls = append(calls, map[string]any{"id": b.ID, "type": "function", "function": map[string]any{"name": b.Name, "arguments": args}})
				}
			}
			if calls != nil {
				am["tool_calls"] = calls
			}
			msgs = append(msgs, am)
			continue
		}
		var tools []map[string]any
		var parts []map[string]any
		image := false
		for _, b := range bs {
			switch b.Type {
			case "text":
				parts = append(parts, map[string]any{"type": "text", "text": b.Text})
			case "image":
				u := b.Source.URL
				if b.Source.Type == "base64" {
					u = "data:" + b.Source.MediaType + ";base64," + b.Source.Data
				}
				parts = append(parts, map[string]any{"type": "image_url", "image_url": map[string]any{"url": u}})
				image = true
			case "tool_result":
				tools = append(tools, map[string]any{"role": "tool", "tool_call_id": b.ToolUseID, "content": joinText(blocks(b.Content))})
			}
		}
		for _, t := range tools { // tool results answer the assistant's calls, so they go first
			msgs = append(msgs, t)
		}
		if len(parts) == 0 {
			continue
		}
		if image {
			msgs = append(msgs, map[string]any{"role": "user", "content": parts})
		} else {
			var ts []string
			for _, p := range parts {
				ts = append(ts, p["text"].(string))
			}
			msgs = append(msgs, map[string]any{"role": "user", "content": strings.Join(ts, "\n")})
		}
	}
	out := map[string]any{"model": in.Model, "messages": msgs, "stream": in.Stream}
	if in.Stream {
		out["stream_options"] = map[string]any{"include_usage": true}
	}
	if in.MaxTokens > 0 {
		out["max_tokens"] = in.MaxTokens
	}
	if in.Temperature != nil {
		out["temperature"] = *in.Temperature
	}
	if in.TopP != nil {
		out["top_p"] = *in.TopP
	}
	if len(in.Stop) > 0 {
		out["stop"] = in.Stop
	}
	if len(in.Tools) > 0 {
		var ts []map[string]any
		for _, t := range in.Tools {
			ts = append(ts, map[string]any{"type": "function", "function": map[string]any{"name": t.Name, "description": t.Description, "parameters": t.InputSchema}})
		}
		out["tools"] = ts
	}
	if tc := in.ToolChoice; tc != nil {
		switch tc.Type {
		case "auto":
			out["tool_choice"] = "auto"
		case "any":
			out["tool_choice"] = "required"
		case "none":
			out["tool_choice"] = "none"
		case "tool":
			out["tool_choice"] = map[string]any{"type": "function", "function": map[string]any{"name": tc.Name}}
		}
	}
	return json.Marshal(out)
}

func stopReason(finish string) string {
	switch finish {
	case "length":
		return "max_tokens"
	case "tool_calls", "function_call":
		return "tool_use"
	}
	return "end_turn"
}

func msgID() string {
	b := make([]byte, 12)
	rand.Read(b)
	return "msg_" + hex.EncodeToString(b)
}

// anthropicPass writes a 200 chat answer to the client in the Messages format. It reports true when the
// provider's stream broke.
func anthropicPass(w http.ResponseWriter, resp *http.Response, t target, ri *reqInfo, stream bool) (providerBroke bool) {
	defer resp.Body.Close()
	model := ri.model
	w.Header().Set("X-Gateway-Provider", t.name+"/"+t.model)
	if !stream {
		var c struct {
			Choices []struct {
				Message struct {
					Content   string `json:"content"`
					ToolCalls []struct {
						ID       string `json:"id"`
						Function struct {
							Name      string `json:"name"`
							Arguments string `json:"arguments"`
						} `json:"function"`
					} `json:"tool_calls"`
				} `json:"message"`
				FinishReason string `json:"finish_reason"`
			} `json:"choices"`
			Usage chatUsage `json:"usage"`
		}
		if err := json.NewDecoder(io.LimitReader(resp.Body, maxBody)).Decode(&c); err != nil || len(c.Choices) == 0 {
			jsonError(w, http.StatusBadGateway, "bad_provider_answer", "The provider's answer was not a chat completion.")
			return true
		}
		ri.inTok, ri.outTok = c.Usage.PromptTokens, c.Usage.CompletionTokens
		ch := c.Choices[0]
		content := []map[string]any{}
		if ch.Message.Content != "" {
			content = append(content, map[string]any{"type": "text", "text": ch.Message.Content})
		}
		for _, tc := range ch.Message.ToolCalls {
			var in json.RawMessage = json.RawMessage(tc.Function.Arguments)
			if !json.Valid(in) {
				in = json.RawMessage("{}")
			}
			content = append(content, map[string]any{"type": "tool_use", "id": tc.ID, "name": tc.Function.Name, "input": in})
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{"id": msgID(), "type": "message", "role": "assistant", "model": model, "content": content,
			"stop_reason": stopReason(ch.FinishReason), "stop_sequence": nil,
			"usage": map[string]any{"input_tokens": c.Usage.PromptTokens, "output_tokens": c.Usage.CompletionTokens}})
		return false
	}

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.WriteHeader(http.StatusOK)
	rc := http.NewResponseController(w)
	send := func(ev string, data map[string]any) bool {
		data["type"] = ev
		b, _ := json.Marshal(data)
		_, err := fmt.Fprintf(w, "event: %s\ndata: %s\n\n", ev, b)
		rc.Flush()
		return err == nil
	}
	var (
		started, open bool
		kind          string // "text" or "tool", the block that is open
		idx           = -1
		tool          = -1 // the provider's index of the open tool call
		finish        string
		usage         chatUsage
		done          bool // saw [DONE] or a finish_reason: the provider ended the answer on purpose
	)
	closeBlock := func() {
		if open {
			send("content_block_stop", map[string]any{"index": idx})
			open = false
		}
	}
	start := func() {
		if !started {
			started = true
			send("message_start", map[string]any{"message": map[string]any{"id": msgID(), "type": "message", "role": "assistant", "model": model,
				"content": []any{}, "stop_reason": nil, "stop_sequence": nil, "usage": map[string]any{"input_tokens": 0, "output_tokens": 0}}})
		}
	}
	sc := bufio.NewScanner(resp.Body)
	sc.Buffer(make([]byte, 64<<10), 1<<20)
	for sc.Scan() {
		data, ok := strings.CutPrefix(sc.Text(), "data:")
		if !ok {
			continue
		}
		data = strings.TrimSpace(data)
		if data == "[DONE]" {
			done = true
			break
		}
		var c struct {
			Choices []struct {
				Delta struct {
					Content   string `json:"content"`
					ToolCalls []struct {
						Index    int    `json:"index"`
						ID       string `json:"id"`
						Function struct {
							Name      string `json:"name"`
							Arguments string `json:"arguments"`
						} `json:"function"`
					} `json:"tool_calls"`
				} `json:"delta"`
				FinishReason string `json:"finish_reason"`
			} `json:"choices"`
			Usage *chatUsage `json:"usage"`
		}
		if json.Unmarshal([]byte(data), &c) != nil {
			continue
		}
		start()
		if c.Usage != nil {
			usage = *c.Usage
		}
		for _, ch := range c.Choices {
			if ch.FinishReason != "" {
				finish, done = ch.FinishReason, true
			}
			if s := ch.Delta.Content; s != "" {
				if !open || kind != "text" {
					closeBlock()
					idx++
					open, kind = true, "text"
					send("content_block_start", map[string]any{"index": idx, "content_block": map[string]any{"type": "text", "text": ""}})
				}
				if !send("content_block_delta", map[string]any{"index": idx, "delta": map[string]any{"type": "text_delta", "text": s}}) {
					return false // client hung up
				}
			}
			for _, tc := range ch.Delta.ToolCalls {
				if !open || kind != "tool" || tc.Index != tool {
					closeBlock()
					idx++
					open, kind, tool = true, "tool", tc.Index
					send("content_block_start", map[string]any{"index": idx, "content_block": map[string]any{"type": "tool_use", "id": tc.ID, "name": tc.Function.Name, "input": map[string]any{}}})
				}
				if a := tc.Function.Arguments; a != "" {
					send("content_block_delta", map[string]any{"index": idx, "delta": map[string]any{"type": "input_json_delta", "partial_json": a}})
				}
			}
		}
	}
	ri.inTok, ri.outTok = usage.PromptTokens, usage.CompletionTokens
	err := sc.Err()
	// A stream that just stops, with no finish_reason and no [DONE], was cut off: say so, never pass it off as finished.
	if (err != nil && !errors.Is(err, context.Canceled)) || (err == nil && !done) {
		start()
		closeBlock()
		send("error", map[string]any{"error": map[string]any{"type": "api_error", "message": "The provider's stream broke."}})
		return true
	}
	start()
	closeBlock()
	send("message_delta", map[string]any{"delta": map[string]any{"stop_reason": stopReason(finish), "stop_sequence": nil},
		"usage": map[string]any{"input_tokens": usage.PromptTokens, "output_tokens": usage.CompletionTokens}})
	send("message_stop", map[string]any{})
	return false
}

// countTokens answers /v1/messages/count_tokens with a rough guess of four bytes a token, which is
// enough for the clients that call it to size their context.
func countTokens(w http.ResponseWriter, r *http.Request) {
	raw, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxBody))
	if err != nil {
		http.Error(w, "request body too large or unreadable", http.StatusRequestEntityTooLarge)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]int{"input_tokens": len(raw)/4 + 1})
}
