package web

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
	"zsched/internal/scheduler"
)

func TestAuthAndControls(t *testing.T) {
	s, e := scheduler.New(t.TempDir())
	if e != nil {
		t.Fatal(e)
	}
	defer s.Close()
	v, e := New(s)
	if e != nil {
		t.Fatal(e)
	}
	for _, c := range []struct {
		token, host, origin string
		want                int
	}{{"", "127.0.0.1:1234", "", 401}, {v.token, "evil.test:1234", "", 403}, {v.token, "127.0.0.1:1234", "http://evil.test", 403}, {v.token, "127.0.0.1:1234", "", 200}} {
		r := httptest.NewRequest("GET", "http://"+c.host+"/api/state", nil)
		r.Header.Set("Authorization", "Bearer "+c.token)
		r.Header.Set("Origin", c.origin)
		w := httptest.NewRecorder()
		v.handler(false).ServeHTTP(w, r)
		if w.Code != c.want {
			t.Fatalf("status %d want %d", w.Code, c.want)
		}
	}
	r := httptest.NewRequest("POST", "http://127.0.0.1:1234/api/action", strings.NewReader(`{"operation":"pause_all","payload":{}}`))
	r.Header.Set("Authorization", "Bearer "+v.token)
	w := httptest.NewRecorder()
	v.handler(false).ServeHTTP(w, r)
	if w.Code != http.StatusOK || !s.Snapshot().Settings.Paused {
		t.Fatal(w.Code, w.Body.String())
	}
}
func TestIndependentDashboard(t *testing.T) {
	s, e := scheduler.New(t.TempDir())
	if e != nil {
		t.Fatal(e)
	}
	defer s.Close()
	cfg := s.Snapshot().Settings
	cfg.WebPort = 0
	s.SetSettings(cfg)
	v, _ := New(s)
	if e = v.Start(); e != nil {
		t.Fatal(e)
	}
	defer v.Close()
	if e = v.StartWeb(); e != nil {
		t.Fatal(e)
	}
	if v.WebURL() == "" {
		t.Fatal("no dashboard")
	}
	if e = v.StopWeb(); e != nil {
		t.Fatal(e)
	}
	if v.WebURL() != "" {
		t.Fatal("dashboard still published")
	}
	req, _ := http.NewRequest("GET", "http://"+v.controlAddr+"/api/state", nil)
	req.Header.Set("Authorization", "Bearer "+v.token)
	res, e := http.DefaultClient.Do(req)
	if e != nil {
		t.Fatal(e)
	}
	res.Body.Close()
	if res.StatusCode != 200 {
		t.Fatal(res.StatusCode)
	}
}

func TestScheduledBuiltinAssistant(t *testing.T) {
	model := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"choices":[{"message":{"content":"Scheduled assistant result"},"finish_reason":"stop"}]}`))
	}))
	defer model.Close()
	s, e := scheduler.New(t.TempDir())
	if e != nil {
		t.Fatal(e)
	}
	defer s.Close()
	cfg := s.Snapshot().Settings
	cfg.AI.URL = model.URL
	cfg.AI.Model = "fake"
	s.SetSettings(cfg)
	_, e = New(s)
	if e != nil {
		t.Fatal(e)
	}
	a, e := s.SaveAgent(scheduler.Agent{Name: "Built in", Binary: "builtin:assistant"})
	if e != nil {
		t.Fatal(e)
	}
	x, e := s.SaveSchedule(scheduler.Schedule{Name: "AI task", AgentID: a.ID, Task: "Inspect schedules", Trigger: scheduler.Trigger{Kind: "interval", EverySeconds: 3600}})
	if e != nil {
		t.Fatal(e)
	}
	r, e := s.RunNow(x.ID)
	if e != nil {
		t.Fatal(e)
	}
	s.Start()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		for _, v := range s.Snapshot().Runs {
			if v.ID == r.ID && v.State == "succeeded" {
				out, e := s.RunLog(r.ID)
				if e != nil || !strings.Contains(out, "Scheduled assistant result") {
					t.Fatal(out, e)
				}
				return
			}
			if v.State == "failed" {
				t.Fatal(v.Error)
			}
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("built-in task did not finish")
}
