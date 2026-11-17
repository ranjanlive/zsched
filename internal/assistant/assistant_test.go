package assistant

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"zsched/internal/scheduler"
)

func TestToolsAndPersistence(t *testing.T) {
	s, e := scheduler.New(t.TempDir())
	if e != nil {
		t.Fatal(e)
	}
	defer s.Close()
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/chat/completions" {
			t.Error(r.URL.Path)
		}
		calls++
		if calls == 1 {
			w.Write([]byte(`{"choices":[{"message":{"role":"assistant","tool_calls":[{"id":"c1","type":"function","function":{"name":"scheduler","arguments":"{\"operation\":\"pause_all\",\"payload\":{}}"}}]},"finish_reason":"tool_calls"}]}`))
		} else {
			w.Write([]byte(`{"choices":[{"message":{"content":"Paused."},"finish_reason":"stop"}]}`))
		}
	}))
	defer server.Close()
	cfg := s.Snapshot().Settings
	cfg.AI.URL = server.URL
	cfg.AI.Model = "fake"
	s.SetSettings(cfg)
	a := New(s)
	a.Action = func(op string, _ json.RawMessage) (any, error) {
		if op != "pause_all" {
			t.Fatal(op)
		}
		c := s.Snapshot().Settings
		c.Paused = true
		return nil, s.SetSettings(c)
	}
	res, e := a.Chat(context.Background(), "Pause all schedules")
	if e != nil || res.Reply != "Paused." || len(res.Actions) != 1 || !s.Snapshot().Settings.Paused {
		t.Fatal(res, e)
	}
	if len(a.History()) != 4 {
		t.Fatal(a.History())
	}
	if allowed("shutdown") || allowed("assistant_key") {
		t.Fatal("privileged operation allowed")
	}
}
func TestEndpointAndErrors(t *testing.T) {
	for _, bad := range []string{"", "file:///tmp/key", "https://key@example.com"} {
		if _, e := endpoint(bad); e == nil {
			t.Fatal(bad)
		}
	}
	s, err := scheduler.New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	a := New(s)
	if _, e := a.Chat(context.Background(), "Hello"); e == nil {
		t.Fatal("unconfigured model accepted")
	}
}
