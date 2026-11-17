package assistant

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"zsched/internal/scheduler"
)

func testCalls() []ToolCall {
	var calls []ToolCall
	for _, op := range []string{"pause_all", "resume_all"} {
		var tc ToolCall
		tc.ID = op
		tc.Type = "function"
		tc.Function.Name = "scheduler"
		tc.Function.Arguments = `{"operation":"` + op + `","payload":{}}`
		calls = append(calls, tc)
	}
	return calls
}

func TestCancellationStopsRemainingActionsAndKeepsValidHistory(t *testing.T) {
	s, e := scheduler.New(t.TempDir())
	if e != nil {
		t.Fatal(e)
	}
	defer s.Close()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"choices": []any{map[string]any{"message": Message{Role: "assistant", ToolCalls: testCalls()}, "finish_reason": "tool_calls"}}})
	}))
	defer server.Close()
	cfg := s.Snapshot().Settings
	cfg.AI.URL = server.URL
	cfg.AI.Model = "fake"
	if e = s.SetSettings(cfg); e != nil {
		t.Fatal(e)
	}
	a := New(s)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	count := 0
	a.ActionContext = func(ctx context.Context, op string, p json.RawMessage) (any, error) {
		count++
		b, e := os.ReadFile(a.HistoryPath)
		if e != nil {
			t.Fatal(e)
		}
		if !strings.Contains(string(b), "pending") {
			t.Fatal("action intent not persisted before execution")
		}
		cancel()
		return nil, nil
	}
	_, e = a.Chat(ctx, "stop after the first action")
	if !errors.Is(e, context.Canceled) || count != 1 {
		t.Fatalf("count %d, error %v", count, e)
	}
	history := a.History()
	if len(history) != 4 || history[3].ToolCallID != "resume_all" || !strings.Contains(history[3].Content, "canceled") {
		t.Fatalf("cancelled call was not answered: %+v", history)
	}
}

func TestRecoveryNeverReplaysUncertainActions(t *testing.T) {
	s, e := scheduler.New(t.TempDir())
	if e != nil {
		t.Fatal(e)
	}
	defer s.Close()
	messages := []Message{{Role: "user", Content: "change schedules"}, {Role: "assistant", ToolCalls: testCalls()},
		{Role: "tool", ToolCallID: "pause_all", Content: `{"execution":"pending"}`},
	}
	b, e := json.Marshal(messages)
	if e != nil {
		t.Fatal(e)
	}
	a := New(s)
	if e = a.Load(b); e != nil {
		t.Fatal(e)
	}
	got := a.History()
	if len(got) != 4 || !strings.Contains(got[2].Content, "uncertain") || !strings.Contains(got[3].Content, "before this action was started") {
		t.Fatalf("unsafe recovery: %+v", got)
	}
	disk, e := os.ReadFile(a.HistoryPath)
	if e != nil {
		t.Fatal(e)
	}
	reloaded := New(s)
	if e = reloaded.Load(disk); e != nil {
		t.Fatal(e)
	}
	if len(reloaded.History()) != 4 {
		t.Fatal("repair was not idempotent")
	}
}

func TestHistoryFailurePreventsActions(t *testing.T) {
	s, e := scheduler.New(t.TempDir())
	if e != nil {
		t.Fatal(e)
	}
	defer s.Close()
	a := New(s)
	a.HistoryPath = s.Home() // cannot atomically replace a directory
	called := false
	a.Action = func(string, json.RawMessage) (any, error) { called = true; return nil, nil }
	if _, e = a.Chat(context.Background(), "change settings"); e == nil || called {
		t.Fatal("action attempted without durable history")
	}
}
