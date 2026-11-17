package assistant

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"path/filepath"
	"strings"
	"sync"
	"time"
	"zsched/internal/scheduler"
)

type Message struct {
	Role       string     `json:"role"`
	Content    string     `json:"content,omitempty"`
	ToolCalls  []ToolCall `json:"tool_calls,omitempty"`
	ToolCallID string     `json:"tool_call_id,omitempty"`
}
type ToolCall struct {
	ID       string `json:"id"`
	Type     string `json:"type"`
	Function struct {
		Name      string `json:"name"`
		Arguments string `json:"arguments"`
	} `json:"function"`
}
type Result struct {
	Reply           string   `json:"reply"`
	Actions         []string `json:"actions"`
	EstimatedTokens int      `json:"estimated_tokens"`
	Compacted       bool     `json:"compacted"`
}
type Assistant struct {
	mu            sync.Mutex
	s             *scheduler.Service
	messages      []Message
	Action        func(string, json.RawMessage) (any, error)
	ActionContext func(context.Context, string, json.RawMessage) (any, error)
	HistoryPath   string
	HTTP          *http.Client
}

func New(s *scheduler.Service) *Assistant {
	return &Assistant{s: s, HistoryPath: filepath.Join(s.Home(), "assistant-history.json"), HTTP: &http.Client{Timeout: 120 * time.Second}}
}
func (a *Assistant) History() []Message {
	a.mu.Lock()
	defer a.mu.Unlock()
	return append([]Message(nil), a.messages...)
}
func (a *Assistant) Clear() error {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.messages = nil
	return a.save()
}
func (a *Assistant) save() error {
	b, e := json.Marshal(a.messages)
	if e != nil {
		return e
	}
	return scheduler.Atomic(a.HistoryPath, b)
}
func (a *Assistant) Load(b []byte) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	var messages []Message
	if err := json.Unmarshal(b, &messages); err != nil {
		return err
	}
	a.messages = repairHistory(messages)
	return a.save()
}
func endpoint(base string) (string, error) {
	u, e := url.Parse(strings.TrimSpace(base))
	if e != nil || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") || u.User != nil {
		return "", errors.New("assistant URL must be an HTTP(S) endpoint without embedded credentials")
	}
	p := strings.TrimRight(u.Path, "/")
	if p == "" {
		p = "/v1"
	}
	if !strings.HasSuffix(p, "/chat/completions") {
		p += "/chat/completions"
	}
	u.Path = p
	return u.String(), nil
}

const instruction = `You are the zsched automation assistant. You have authorized access to manage every registered agent, schedule, run, notification and scheduler setting through tools. Execute the user's requested changes using the tools, then explain what actually succeeded. Never claim an action happened without a successful tool result. Do not invent agent IDs or binaries; inspect state first. Do not delete or stop unrelated work. Dates use the schedule timezone; ask for missing task intent. Default agent home is the binary directory and workspace is home/workspace. Random weekday scheduling uses a same-day window. You cannot access API keys or arbitrary files. Tool schemas and examples:
agent_save payload: {name,description,binary,home,workspace,max_parallel_run}; editing also needs id and enabled. agent_delete payload: {id}.
schedule_save payload: {id?,name,agent_id,task,enabled:true,trigger:{kind,timezone,at?,every_seconds?,min_seconds?,max_seconds?,days?,time?,window_start?,window_end?},overlap:"skip"|"queue",missed_runs:"run_once"|"skip",timeout_minutes:60,retry_count:0,retry_delay_seconds:300,session_mode:"fresh"|"persistent"}. Trigger kinds once (RFC3339 at), interval (seconds), random_interval (min/max seconds), daily (HH:MM), weekly (days 0=Sunday to 6=Saturday, HH:MM), random_weekly (days and HH:MM window).
schedule_pause/resume/delete/run payload {id}. run_stop/log/open payload {id}; open also ui:"web"|"cli". agent_open {id,ui}. settings_save: fetch full settings from state, modify and return the entire settings object. pause_all/resume_all/stop_all/notifications_read/history_clear/web_start/web_stop take {}. state and notification list come from state tool. Audit actions are recorded.`

func tool() any {
	return map[string]any{"type": "function", "function": map[string]any{"name": "scheduler", "description": "Inspect or modify zsched. Read state first for IDs and current settings. Mutation payloads follow the system instructions.", "parameters": map[string]any{"type": "object", "required": []string{"operation", "payload"}, "additionalProperties": false, "properties": map[string]any{"operation": map[string]any{"type": "string", "enum": []string{"state", "agent_save", "agent_delete", "agent_open", "schedule_save", "schedule_pause", "schedule_resume", "schedule_delete", "schedule_run", "run_stop", "run_log", "run_open", "settings_save", "pause_all", "resume_all", "stop_all", "notifications_read", "history_clear", "web_start", "web_stop"}}, "payload": map[string]any{"type": "object", "additionalProperties": true}}}}}
}
func estimate(ms []Message) int { b, _ := json.Marshal(ms); return len(b) / 4 }
func (a *Assistant) call(ctx context.Context, ms []Message, tools bool) (Message, error) {
	cfg := a.s.Snapshot().Settings.AI
	if cfg.Model == "" {
		return Message{}, errors.New("configure assistant URL and model in Settings first")
	}
	ep, e := endpoint(cfg.URL)
	if e != nil {
		return Message{}, e
	}
	key, e := a.s.Key()
	if e != nil {
		return Message{}, e
	}
	body := map[string]any{"model": cfg.Model, "messages": ms, "stream": false}
	if tools {
		body["tools"] = []any{tool()}
		body["tool_choice"] = "auto"
	}
	raw, _ := json.Marshal(body)
	req, e := http.NewRequestWithContext(ctx, "POST", ep, bytes.NewReader(raw))
	if e != nil {
		return Message{}, e
	}
	req.Header.Set("Content-Type", "application/json")
	if key != "" {
		req.Header.Set("Authorization", "Bearer "+key)
	}
	resp, e := a.HTTP.Do(req)
	if e != nil {
		if ctx.Err() != nil {
			return Message{}, ctx.Err()
		}
		return Message{}, errors.New("cannot reach assistant endpoint")
	}
	defer resp.Body.Close()
	b, e := io.ReadAll(io.LimitReader(resp.Body, 8*1024*1024+1))
	if e != nil {
		return Message{}, e
	}
	if len(b) > 8*1024*1024 {
		return Message{}, errors.New("assistant response exceeds 8 MB")
	}
	if resp.StatusCode/100 != 2 {
		return Message{}, fmt.Errorf("assistant endpoint returned HTTP %d; check URL, key and model", resp.StatusCode)
	}
	var out struct {
		Choices []struct {
			Message Message `json:"message"`
			Finish  string  `json:"finish_reason"`
		} `json:"choices"`
	}
	if e = json.Unmarshal(b, &out); e != nil {
		return Message{}, errors.New("endpoint returned invalid chat completion JSON")
	}
	if len(out.Choices) == 0 {
		return Message{}, errors.New("endpoint returned no answer")
	}
	v := out.Choices[0]
	if v.Finish == "length" || v.Finish == "content_filter" {
		return Message{}, fmt.Errorf("assistant response stopped: %s", v.Finish)
	}
	v.Message.Role = "assistant"
	seen := map[string]bool{}
	for _, call := range v.Message.ToolCalls {
		if call.ID == "" || seen[call.ID] || call.Type != "function" {
			return Message{}, errors.New("endpoint returned invalid or duplicate function-call IDs")
		}
		seen[call.ID] = true
	}
	if v.Message.Content == "" && len(v.Message.ToolCalls) == 0 {
		return Message{}, errors.New("endpoint returned an empty answer")
	}
	return v.Message, nil
}
func (a *Assistant) Chat(ctx context.Context, text string) (Result, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	result := Result{Actions: []string{}}
	text = strings.TrimSpace(text)
	if text == "" || len(text) > 100000 {
		return result, errors.New("message must contain 1–100000 bytes")
	}
	// Also repair in-memory history after a failed write in a previous turn.
	a.messages = repairHistory(a.messages)
	ctx, cancel := context.WithTimeout(ctx, 5*time.Minute)
	defer cancel()
	cfg := a.s.Snapshot().Settings.AI
	if estimate(a.messages)+len(text)/4 >= cfg.CompactAt && len(a.messages) > 2 {
		ms := append([]Message{{Role: "system", Content: "Summarize this scheduler conversation for continuation. Preserve user intent and exact IDs, schedules, outcomes and unfinished work. Return summary only. Do not call tools."}}, a.messages...)
		m, e := a.call(ctx, ms, false)
		if e != nil {
			return result, fmt.Errorf("cannot compact assistant history: %w", e)
		}
		a.messages = []Message{{Role: "user", Content: "Earlier conversation summary:\n" + m.Content}}
		result.Compacted = true
	}
	a.messages = append(a.messages, Message{Role: "user", Content: text})
	if err := a.save(); err != nil {
		return result, err
	}
	for round := 0; round < 24; round++ {
		if ctx.Err() != nil {
			return result, ctx.Err()
		}
		st := a.s.Snapshot()
		snapshot, _ := json.Marshal(st)
		if len(snapshot) > 160000 {
			snapshot = []byte(`{"note":"State is large; call state for current data."}`)
		}
		system := Message{Role: "system", Content: instruction + "\nCurrent local time: " + time.Now().Format(time.RFC3339) + "\nCurrent state:\n" + string(snapshot)}
		ms := append([]Message{system}, a.messages...)
		if estimate(ms) > cfg.ContextLimit {
			return result, errors.New("assistant context exceeds its configured limit; clear conversation or increase context")
		}
		m, e := a.call(ctx, ms, true)
		if e != nil {
			a.save()
			return result, e
		}
		a.messages = append(a.messages, m)
		// Persist every declared call before any mutation. A restart can then
		// distinguish an unstarted call from a call whose outcome is uncertain.
		if e := a.save(); e != nil {
			return result, e
		}
		if len(m.ToolCalls) == 0 {
			result.Reply = m.Content
			result.EstimatedTokens = estimate(a.messages)
			return result, a.save()
		}
		for _, tc := range m.ToolCalls {
			index := len(a.messages)
			a.messages = append(a.messages, Message{Role: "tool", ToolCallID: tc.ID, Content: `{"execution":"pending","error":"Action outcome is not yet recorded"}`})
			if e := a.save(); e != nil {
				return result, e
			}
			var args struct {
				Operation string          `json:"operation"`
				Payload   json.RawMessage `json:"payload"`
			}
			var out any
			var e error
			if ctx.Err() != nil {
				e = ctx.Err()
			} else if tc.Function.Name != "scheduler" {
				e = errors.New("unknown tool")
			} else if e = json.Unmarshal([]byte(tc.Function.Arguments), &args); e == nil {
				if !allowed(args.Operation) {
					e = errors.New("operation is not available to the assistant")
				} else if args.Operation == "state" {
					out = a.s.Snapshot()
				} else {
					if a.ActionContext != nil {
						out, e = a.ActionContext(ctx, args.Operation, args.Payload)
					} else if a.Action != nil {
						out, e = a.Action(args.Operation, args.Payload)
					} else {
						e = errors.New("scheduler actions are unavailable")
					}
				}
			}
			data := map[string]any{"result": out}
			if e != nil {
				data = map[string]any{"error": e.Error()}
			} else {
				result.Actions = append(result.Actions, args.Operation)
			}
			b, _ := json.Marshal(data)
			a.messages[index].Content = string(b)
			if e := a.save(); e != nil {
				return result, e
			}
		}
		if e = a.save(); e != nil {
			return result, e
		}
		if ctx.Err() != nil {
			return result, ctx.Err()
		}
	}
	return result, errors.New("assistant reached its 24-round tool limit; send a follow-up to continue")
}

func allowed(op string) bool {
	switch op {
	case "state", "agent_save", "agent_delete", "agent_open", "schedule_save", "schedule_pause", "schedule_resume", "schedule_delete", "schedule_run", "run_stop", "run_log", "run_open", "settings_save", "pause_all", "resume_all", "stop_all", "notifications_read", "history_clear", "web_start", "web_stop":
		return true
	}
	return false
}
