// Package integration verifies the production scheduler/agent process contract.
package integration

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"
	"zsched/internal/scheduler"
)

func TestRealZagentContract(t *testing.T) {
	root := t.TempDir()
	binary := filepath.Join(root, "zagent")
	if runtime.GOOS == "windows" {
		binary += ".exe"
	}
	buildCtx, cancelBuild := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancelBuild()
	agentDir := os.Getenv("ZAGENT_DIR")
	if agentDir == "" {
		for _, cand := range []string{
			filepath.Join("..", "..", "..", "zengine"),
			filepath.Join("..", "..", "..", "zagent"),
			filepath.Join("..", "..", "..", "zagent_0.1.1"),
			filepath.Join("..", "..", "..", "Release", "zagent_0.1.1"),
		} {
			if _, err := os.Stat(filepath.Join(cand, "cmd", "zagent")); err == nil {
				agentDir = cand
				break
			}
		}
	}
	if agentDir == "" {
		t.Skip("skipping integration test: zagent source directory not found")
	}
	build := exec.CommandContext(buildCtx, "go", "build", "-o", binary, "./cmd/zagent")
	build.Dir = agentDir
	if output, e := build.CombinedOutput(); e != nil {
		t.Fatalf("build actual agent: %v\n%s", e, output)
	}
	var mu sync.Mutex
	var requests []string
	blocking := false
	started := make(chan struct{}, 1)
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body json.RawMessage
		if e := json.NewDecoder(r.Body).Decode(&body); e != nil {
			http.Error(w, e.Error(), 400)
			return
		}
		mu.Lock()
		requests = append(requests, string(body))
		block := blocking
		mu.Unlock()
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"content\":\"REAL_AGENT_COMPLETED\"},\"finish_reason\":null}]}\n\n")
		w.(http.Flusher).Flush()
		if block {
			select {
			case started <- struct{}{}:
			default:
			}
			<-r.Context().Done()
			return
		}
		fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n")
	}))
	defer provider.Close()
	agentHome := filepath.Join(root, "agent")
	workspace := filepath.Join(root, "workspace")
	for _, path := range []string{agentHome, workspace} {
		if e := os.MkdirAll(path, 0700); e != nil {
			t.Fatal(e)
		}
	}
	env := "PROVIDER=openai_compatible\nBASE_URL=" + provider.URL + "\nMODEL=fake\nAPI_KEY=synthetic\n"
	if e := os.WriteFile(filepath.Join(agentHome, ".env"), []byte(env), 0600); e != nil {
		t.Fatal(e)
	}
	if e := os.WriteFile(filepath.Join(agentHome, "config.json"), []byte(`{"permissions":{"run_command":"deny"}}`), 0600); e != nil {
		t.Fatal(e)
	}
	service, e := scheduler.New(filepath.Join(root, "scheduler"))
	if e != nil {
		t.Fatal(e)
	}
	service.Start()
	defer service.Close()
	agent, e := service.SaveAgent(scheduler.Agent{Name: "Real zagent", Binary: binary, Home: agentHome, Workspace: workspace, MaxParallel: 2})
	if e != nil {
		t.Fatal(e)
	}
	makeSchedule := func(name, mode string) scheduler.Schedule {
		t.Helper()
		s, e := service.SaveSchedule(scheduler.Schedule{Name: name, AgentID: agent.ID, Task: "CONTRACT_TASK", SessionMode: mode, Trigger: scheduler.Trigger{Kind: "once", At: time.Now().Add(time.Hour), Timezone: "UTC"}})
		if e != nil {
			t.Fatal(e)
		}
		return s
	}
	waitRun := func(id, state string) scheduler.Run {
		t.Helper()
		deadline := time.Now().Add(30 * time.Second)
		for {
			for _, r := range service.Snapshot().Runs {
				if r.ID == id {
					if r.State == state {
						return r
					}
					if r.State == "failed" || r.State == "timed_out" {
						log, _ := service.RunLog(id)
						t.Fatalf("run failed: %s\n%s", r.Error, log)
					}
				}
			}
			if time.Now().After(deadline) {
				t.Fatalf("run %s did not reach %s", id, state)
			}
			time.Sleep(20 * time.Millisecond)
		}
	}
	persistent := makeSchedule("Persistent", "persistent")
	first, e := service.RunNow(persistent.ID)
	if e != nil {
		t.Fatal(e)
	}
	waitRun(first.ID, "succeeded")
	second, e := service.RunNow(persistent.ID)
	if e != nil {
		t.Fatal(e)
	}
	waitRun(second.ID, "succeeded")
	if first.SessionID != second.SessionID {
		t.Fatal("persistent session changed")
	}
	mu.Lock()
	secondBody := requests[1]
	mu.Unlock()
	if !strings.Contains(secondBody, "REAL_AGENT_COMPLETED") {
		t.Fatal("persistent agent did not resume history")
	}
	fresh := makeSchedule("Fresh", "fresh")
	fresh1, e := service.RunNow(fresh.ID)
	if e != nil {
		t.Fatal(e)
	}
	waitRun(fresh1.ID, "succeeded")
	fresh2, e := service.RunNow(fresh.ID)
	if e != nil {
		t.Fatal(e)
	}
	waitRun(fresh2.ID, "succeeded")
	if fresh1.SessionID == fresh2.SessionID {
		t.Fatal("fresh sessions reused")
	}
	log, e := service.RunLog(fresh2.ID)
	if e != nil || !strings.Contains(log, "REAL_AGENT_COMPLETED") {
		t.Fatalf("real-agent output missing: %v %s", e, log)
	}
	mu.Lock()
	blocking = true
	mu.Unlock()
	stopped, e := service.RunNow(fresh.ID)
	if e != nil {
		t.Fatal(e)
	}
	select {
	case <-started:
	case <-time.After(30 * time.Second):
		t.Fatal("blocking agent never reached provider")
	}
	// A headless scheduled agent publishes the same authenticated API used by UIs.
	var endpoint struct{ Addr, Token string }
	b, e := os.ReadFile(filepath.Join(agentHome, "sessions", stopped.SessionID, "lock.json"))
	if e != nil {
		t.Fatal(e)
	}
	if e = json.Unmarshal(b, &endpoint); e != nil {
		t.Fatal(e)
	}
	request, e := http.NewRequest("GET", "http://"+endpoint.Addr+"/api/state", nil)
	if e != nil {
		t.Fatal(e)
	}
	request.Header.Set("Authorization", "Bearer "+endpoint.Token)
	client := &http.Client{Timeout: 5 * time.Second}
	response, e := client.Do(request)
	if e != nil {
		t.Fatal(e)
	}
	response.Body.Close()
	if response.StatusCode != 200 {
		t.Fatalf("cannot attach to scheduled agent: HTTP %d", response.StatusCode)
	}
	if e = service.StopRun(stopped.ID); e != nil {
		t.Fatal(e)
	}
	waitRun(stopped.ID, "cancelled")
}
