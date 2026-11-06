// OpenAI Responses API (/v1/responses) in front of the same chat-completions pipeline, so Codex CLI and other
// Responses clients can use any provider. Like anthropic.go: the request becomes a chat body before the filter
// sees it, and the answer (plain or streamed) is translated back.
// ponytail: text, images and function tools only. Reasoning items, web search, local shell and freeform tools are
// dropped, and the answer carries no reasoning items. A conversation is whatever the client sends each time:
// previous_response_id and stored responses are not supported.
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
	"time"
)

func rid(prefix string) string {
	b := make([]byte, 12)
	rand.Read(b)
	return prefix + "_" + hex.EncodeToString(b)
}

// rText reads a message content field: a string, or a list of parts with text and images.
func rText(raw json.RawMessage) (text string, parts []map[string]any, image bool) {
	var s string
	if json.Unmarshal(raw, &s) == nil {
		return s, nil, false
	}
	var ps []struct {
		Type     string          `json:"type"`
		Text     string          `json:"text"`
		ImageURL json.RawMessage `json:"image_url"`
	}
	json.Unmarshal(raw, &ps)
	var ts []string
	for _, p := range ps {
		switch p.Type {
		case "input_text", "output_text", "text":
			ts = append(ts, p.Text)
			parts = append(parts, map[string]any{"type": "text", "text": p.Text})
		case "input_image":
			var u string
			if json.Unmarshal(p.ImageURL, &u) != nil {
				var o struct {
					URL string `json:"url"`
				}
				json.Unmarshal(p.ImageURL, &o)
				u = o.URL
			}
			parts = append(parts, map[string]any{"type": "image_url", "image_url": map[string]any{"url": u}})
			image = true
		}
	}
	return strings.Join(ts, "\n"), parts, image
}

// responsesToChat turns a Responses request into a chat-completions body.
func responsesToChat(raw []byte) ([]byte, error) {
	var in struct {
		Model        string          `json:"model"`
		Instructions string          `json:"instructions"`
		Input        json.RawMessage `json:"input"`
		MaxOutput    int             `json:"max_output_tokens"`
		Temperature  *float64        `json:"temperature"`
		TopP         *float64        `json:"top_p"`
		Stream       bool            `json:"stream"`
		Tools        []struct {
			Type        string          `json:"type"`
			Name        string          `json:"name"`
			Description string          `json:"description"`
			Parameters  json.RawMessage `json:"parameters"`
		} `json:"tools"`
		ToolChoice json.RawMessage `json:"tool_choice"`
	}
	if err := json.Unmarshal(raw, &in); err != nil {
		return nil, err
	}
	var msgs []map[string]any
	if in.Instructions != "" {
		msgs = append(msgs, map[string]any{"role": "system", "content": in.Instructions})
	}
	var items []struct {
		Type      string          `json:"type"`
		Role      string          `json:"role"`
		Content   json.RawMessage `json:"content"`
		CallID    string          `json:"call_id"`
		Name      string          `json:"name"`
		Arguments string          `json:"arguments"`
		Output    json.RawMessage `json:"output"`
	}
	var plain string
	if json.Unmarshal(in.Input, &plain) == nil {
		msgs = append(msgs, map[string]any{"role": "user", "content": plain})
	} else if err := json.Unmarshal(in.Input, &items); err != nil {
		return nil, err
	}
	for _, it := range items {
		switch it.Type {
		case "", "message":
			role := it.Role
			if role == "developer" {
				role = "system"
			}
			text, parts, image := rText(it.Content)
			if image && role == "user" {
				msgs = append(msgs, map[string]any{"role": role, "content": parts})
			} else {
				msgs = append(msgs, map[string]any{"role": role, "content": text})
			}
		case "function_call":
			args := it.Arguments
			if args == "" {
				args = "{}"
			}
			call := map[string]any{"id": it.CallID, "type": "function", "function": map[string]any{"name": it.Name, "arguments": args}}
			if n := len(msgs); n > 0 && msgs[n-1]["role"] == "assistant" { // a text message and its calls are one chat message
				calls, _ := msgs[n-1]["tool_calls"].([]map[string]any)
				msgs[n-1]["tool_calls"] = append(calls, call)
			} else {
				msgs = append(msgs, map[string]any{"role": "assistant", "content": nil, "tool_calls": []map[string]any{call}})
			}
		case "function_call_output":
			out, _, _ := rText(it.Output)
			if out == "" {
				out = strings.TrimSpace(string(it.Output))
				if out == "null" {
					out = ""
				}
			}
			msgs = append(msgs, map[string]any{"role": "tool", "tool_call_id": it.CallID, "content": out})
		}
	}
	out := map[string]any{"model": in.Model, "messages": msgs, "stream": in.Stream}
	if in.Stream {
		out["stream_options"] = map[string]any{"include_usage": true}
	}
	if in.MaxOutput > 0 {
		out["max_tokens"] = in.MaxOutput
	}
	if in.Temperature != nil {
		out["temperature"] = *in.Temperature
	}
	if in.TopP != nil {
		out["top_p"] = *in.TopP
	}
	var tools []map[string]any
	for _, t := range in.Tools {
		if t.Type != "function" {
			continue
		}
		params := t.Parameters
		if len(params) == 0 || string(params) == "null" {
			params = json.RawMessage(`{"type":"object","properties":{}}`)
		}
		tools = append(tools, map[string]any{"type": "function", "function": map[string]any{"name": t.Name, "description": t.Description, "parameters": params}})
	}
	if tools != nil {
		out["tools"] = tools
		var s string
		var o struct {
			Type, Name string
		}
		if json.Unmarshal(in.ToolChoice, &s) == nil && s != "" {
			out["tool_choice"] = s
		} else if json.Unmarshal(in.ToolChoice, &o) == nil && o.Type == "function" && o.Name != "" {
			out["tool_choice"] = map[string]any{"type": "function", "function": map[string]any{"name": o.Name}}
		}
	}
	return json.Marshal(out)
}

// responsesPass writes a 200 chat answer to the client in the Responses format. It reports true when the
// provider's stream broke.
func responsesPass(w http.ResponseWriter, resp *http.Response, t target, ri *reqInfo, stream bool) (providerBroke bool) {
	defer resp.Body.Close()
	model := ri.model
	respID, created := rid("resp"), time.Now().Unix()
	w.Header().Set("X-Gateway-Provider", t.name+"/"+t.model)
	// envelope is the response object, with whatever output and usage are known.
	envelope := func(status string, output []map[string]any, u chatUsage) map[string]any {
		if output == nil {
			output = []map[string]any{}
		}
		return map[string]any{"id": respID, "object": "response", "created_at": created, "status": status, "model": model, "output": output,
			"usage": map[string]any{"input_tokens": u.PromptTokens, "output_tokens": u.CompletionTokens, "total_tokens": u.PromptTokens + u.CompletionTokens}}
	}
	msgItem := func(id, text, status string) map[string]any {
		return map[string]any{"type": "message", "id": id, "role": "assistant", "status": status,
			"content": []map[string]any{{"type": "output_text", "text": text, "annotations": []any{}}}}
	}
	callItem := func(id, callID, name, args, status string) map[string]any {
		return map[string]any{"type": "function_call", "id": id, "call_id": callID, "name": name, "arguments": args, "status": status}
	}

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
			} `json:"choices"`
			Usage chatUsage `json:"usage"`
		}
		if err := json.NewDecoder(io.LimitReader(resp.Body, maxBody)).Decode(&c); err != nil || len(c.Choices) == 0 {
			jsonError(w, http.StatusBadGateway, "bad_provider_answer", "The provider's answer was not a chat completion.")
			return true
		}
		ri.inTok, ri.outTok = c.Usage.PromptTokens, c.Usage.CompletionTokens
		m := c.Choices[0].Message
		var out []map[string]any
		if m.Content != "" {
			out = append(out, msgItem(rid("msg"), m.Content, "completed"))
		}
		for _, tc := range m.ToolCalls {
			out = append(out, callItem(rid("fc"), tc.ID, tc.Function.Name, tc.Function.Arguments, "completed"))
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(envelope("completed", out, c.Usage))
		return false
	}

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.WriteHeader(http.StatusOK)
	rc := http.NewResponseController(w)
	seq := 0
	send := func(ev string, data map[string]any) bool {
		data["type"], data["sequence_number"] = ev, seq
		seq++
		b, _ := json.Marshal(data)
		_, err := fmt.Fprintf(w, "event: %s\ndata: %s\n\n", ev, b)
		rc.Flush()
		return err == nil
	}
	var (
		output  []map[string]any // finished items
		started bool
		kind    string // "text" or "tool", the item that is open ("" when none)
		itemID  string
		callID  string
		name    string
		text    strings.Builder // text or arguments of the open item
		tool    = -1            // the provider's index of the open tool call
		usage   chatUsage
		idx     = -1
	)
	closeItem := func() {
		switch kind {
		case "text":
			send("response.output_text.done", map[string]any{"item_id": itemID, "output_index": idx, "content_index": 0, "text": text.String()})
			send("response.content_part.done", map[string]any{"item_id": itemID, "output_index": idx, "content_index": 0, "part": map[string]any{"type": "output_text", "text": text.String(), "annotations": []any{}}})
			it := msgItem(itemID, text.String(), "completed")
			output = append(output, it)
			send("response.output_item.done", map[string]any{"output_index": idx, "item": it})
		case "tool":
			send("response.function_call_arguments.done", map[string]any{"item_id": itemID, "output_index": idx, "arguments": text.String()})
			it := callItem(itemID, callID, name, text.String(), "completed")
			output = append(output, it)
			send("response.output_item.done", map[string]any{"output_index": idx, "item": it})
		}
		kind = ""
		text.Reset()
	}
	start := func() {
		if !started {
			started = true
			send("response.created", map[string]any{"response": envelope("in_progress", nil, chatUsage{})})
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
			if s := ch.Delta.Content; s != "" {
				if kind != "text" {
					closeItem()
					idx++
					kind, itemID = "text", rid("msg")
					send("response.output_item.added", map[string]any{"output_index": idx, "item": map[string]any{"type": "message", "id": itemID, "role": "assistant", "status": "in_progress", "content": []any{}}})
					send("response.content_part.added", map[string]any{"item_id": itemID, "output_index": idx, "content_index": 0, "part": map[string]any{"type": "output_text", "text": "", "annotations": []any{}}})
				}
				text.WriteString(s)
				if !send("response.output_text.delta", map[string]any{"item_id": itemID, "output_index": idx, "content_index": 0, "delta": s}) {
					return false // client hung up
				}
			}
			for _, tc := range ch.Delta.ToolCalls {
				if kind != "tool" || tc.Index != tool {
					closeItem()
					idx++
					kind, tool, itemID, callID, name = "tool", tc.Index, rid("fc"), tc.ID, tc.Function.Name
					send("response.output_item.added", map[string]any{"output_index": idx, "item": callItem(itemID, callID, name, "", "in_progress")})
				}
				if a := tc.Function.Arguments; a != "" {
					text.WriteString(a)
					send("response.function_call_arguments.delta", map[string]any{"item_id": itemID, "output_index": idx, "delta": a})
				}
			}
		}
	}
	ri.inTok, ri.outTok = usage.PromptTokens, usage.CompletionTokens
	if err := sc.Err(); err != nil && !errors.Is(err, context.Canceled) {
		start()
		r := envelope("failed", output, usage)
		r["error"] = map[string]any{"code": "provider_stream_error", "message": "The provider's stream broke."}
		send("response.failed", map[string]any{"response": r})
		return true
	}
	start()
	closeItem()
	send("response.completed", map[string]any{"response": envelope("completed", output, usage)})
	return false
}
