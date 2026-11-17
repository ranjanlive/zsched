package scheduler

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
	"zsched/internal/platform"
)

func TestTriggers(t *testing.T) {
	now := time.Date(2026, 10, 12, 1, 0, 0, 0, time.UTC)
	cases := []struct {
		tr   Trigger
		want time.Time
	}{
		{Trigger{Kind: "interval", EverySeconds: 3600}, now.Add(time.Hour)},
		{Trigger{Kind: "random_interval", MinSeconds: 72000, MaxSeconds: 108000}, now.Add(30 * time.Hour)},
		{Trigger{Kind: "weekly", Days: []int{6}, Time: "09:00", Timezone: "Asia/Kolkata"}, time.Date(2026, 10, 17, 3, 30, 0, 0, time.UTC)},
		{Trigger{Kind: "random_weekly", Days: []int{6}, WindowStart: "10:00", WindowEnd: "12:00", Timezone: "Asia/Kolkata"}, time.Date(2026, 10, 17, 6, 30, 0, 0, time.UTC)},
		{Trigger{Kind: "once", At: now.Add(time.Hour)}, now.Add(time.Hour)},
	}
	for _, c := range cases {
		n, e := Next(c.tr, now, func(n int64) int64 { return n })
		if e != nil || !n.Equal(c.want) {
			t.Fatalf("%s: %v %v want %v", c.tr.Kind, n, e, c.want)
		}
	}
	n, e := Next(Trigger{Kind: "daily", Time: "02:30", Timezone: "America/New_York"}, time.Date(2026, 3, 8, 5, 0, 0, 0, time.UTC), nil)
	if e != nil || n.Day() != 9 {
		t.Fatalf("DST missing time: %v %v", n, e)
	}
	for _, tr := range []Trigger{{Kind: "interval", EverySeconds: 0}, {Kind: "random_interval", MinSeconds: 2, MaxSeconds: 1}, {Kind: "weekly", Days: []int{9}, Time: "00:00"}, {Kind: "random_weekly", Days: []int{6}, WindowStart: "23:00", WindowEnd: "01:00"}, {Kind: "daily", Time: "25:00"}} {
		if tr.Validate() == nil {
			t.Fatal("invalid trigger accepted", tr)
		}
	}
}
func service(t *testing.T) *Service {
	t.Helper()
	s, e := New(t.TempDir())
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(s.Close)
	return s
}
func agent(t *testing.T, s *Service) Agent {
	t.Helper()
	exe, e := os.Executable()
	if e != nil {
		t.Fatal(e)
	}
	a, e := s.SaveAgent(Agent{Name: "test", Binary: exe, Home: t.TempDir(), MaxParallel: 1})
	if e != nil {
		t.Fatal(e)
	}
	if a.Workspace != filepath.Join(a.Home, "workspace") {
		t.Fatal(a.Workspace)
	}
	return a
}
func schedule(t *testing.T, s *Service, a Agent) Schedule {
	t.Helper()
	x, e := s.SaveSchedule(Schedule{Name: "test", AgentID: a.ID, Task: "safe", Trigger: Trigger{Kind: "interval", EverySeconds: 3600}})
	if e != nil {
		t.Fatal(e)
	}
	return x
}
func TestPausedQueueAndLimits(t *testing.T) {
	s := service(t)
	a := agent(t, s)
	x := schedule(t, s, a)
	r, e := s.RunNow(x.ID)
	if e != nil {
		t.Fatal(e)
	}
	s.mu.Lock()
	s.state.Runs[0].Manual = false
	s.mu.Unlock()
	if e = s.PauseSchedule(x.ID, true); e != nil {
		t.Fatal(e)
	}
	s.tick(time.Now())
	if s.Snapshot().Runs[0].State != "queued" {
		t.Fatal("paused run launched")
	}
	if e = s.StopRun(r.ID); e != nil {
		t.Fatal(e)
	}
	if s.Snapshot().Runs[0].State != "cancelled" {
		t.Fatal("queue not cancelled")
	}
}
func TestAtomicRollbackAndRecovery(t *testing.T) {
	s := service(t)
	a := agent(t, s)
	x := schedule(t, s, a)
	s.mu.Lock()
	s.state.Runs = append(s.state.Runs, s.makeRun(x, time.Now()))
	s.state.Runs[0].State = "running"
	e := s.persist()
	s.mu.Unlock()
	if e != nil {
		t.Fatal(e)
	}
	other, e := New(s.Home())
	if e != nil {
		t.Fatal(e)
	}
	defer other.Close()
	if other.Snapshot().Runs[0].State != "interrupted" {
		t.Fatal("crash recovery replayed work")
	}
	before := s.Snapshot().Settings
	orig := s.home
	s.home = filepath.Join(s.home, "block")
	os.WriteFile(s.home, []byte("block"), 0600)
	cfg := before
	cfg.Paused = true
	if s.SetSettings(cfg) == nil {
		t.Fatal("expected write error")
	}
	s.home = orig
	if s.Snapshot().Settings.Paused != before.Paused {
		t.Fatal("failed transaction modified state")
	}
}
func TestLockAndKey(t *testing.T) {
	p := filepath.Join(t.TempDir(), "lock")
	l, e := platform.Acquire(p)
	if e != nil {
		t.Fatal(e)
	}
	if other, e := platform.Acquire(p); e == nil {
		other.Close()
		t.Fatal("duplicate daemon lock")
	}
	l.Close()
	l, e = platform.Acquire(p)
	if e != nil {
		t.Fatal(e)
	}
	l.Close()
	s := service(t)
	if e = s.SetKey("test-secret"); e != nil {
		t.Fatal(e)
	}
	key, e := s.Key()
	if e != nil || key != "test-secret" {
		t.Fatalf("key roundtrip %q %v", key, e)
	}
}
func TestWorkerLifecycle(t *testing.T) {
	// Build a real fake binary using the exact external-agent flag contract.
	dir := t.TempDir()
	src := filepath.Join(dir, "fake.go")
	code := `package main;import("flag";"os";"fmt";"time");func main(){flag.String("home","","");flag.String("workspace","","");flag.String("session","","");p:=flag.String("prompt-file","","");flag.Bool("verbose",false,"");flag.Parse();b,_:=os.ReadFile(*p);fmt.Print(string(b));if string(b)=="wait"{time.Sleep(30*time.Second)}else{time.Sleep(300*time.Millisecond)}}`
	os.WriteFile(src, []byte(code), 0600)
	exe := filepath.Join(dir, "fake.exe")
	cmd := exec.Command("go", "build", "-o", exe, src)
	if b, e := cmd.CombinedOutput(); e != nil {
		t.Fatalf("fake build: %s %v", b, e)
	}
	s := service(t)
	a, e := s.SaveAgent(Agent{Name: "fake", Binary: exe, Home: dir})
	if e != nil {
		t.Fatal(e)
	}
	x := schedule(t, s, a)
	x.Task = "success"
	x, e = s.SaveSchedule(x)
	if e != nil {
		t.Fatal(e)
	}
	r, e := s.RunNow(x.ID)
	if e != nil {
		t.Fatal(e)
	}
	s.Start()
	waitState(t, s, r.ID, "succeeded")
	log, e := s.RunLog(r.ID)
	if e != nil || !strings.Contains(log, "success") {
		t.Fatal(log, e)
	}
	x.Task = "wait"
	x, e = s.SaveSchedule(x)
	if e != nil {
		t.Fatal(e)
	}
	r, e = s.RunNow(x.ID)
	if e != nil {
		t.Fatal(e)
	}
	waitState(t, s, r.ID, "running")
	if e = s.StopRun(r.ID); e != nil {
		t.Fatal(e)
	}
	waitState(t, s, r.ID, "cancelled")
}
func waitState(t *testing.T, s *Service, id, want string) {
	t.Helper()
	ctx, c := context.WithTimeout(context.Background(), 15*time.Second)
	defer c()
	for {
		for _, r := range s.Snapshot().Runs {
			if r.ID == id && r.State == want {
				return
			}
			if r.ID == id && r.State == "failed" {
				t.Fatalf("worker failed: %s", r.Error)
			}
		}
		select {
		case <-ctx.Done():
			t.Fatalf("waiting for %s: %+v", want, s.Snapshot().Runs)
		case <-time.After(20 * time.Millisecond):
		}
	}
}

func TestDispatchCapacityAndPersistentSession(t *testing.T) {
	s := service(t)
	a := agent(t, s)
	a.MaxParallel = 2
	s.SaveAgent(a)
	x := schedule(t, s, a)
	x.SessionMode = "persistent"
	x.Overlap = "queue"
	x, e := s.SaveSchedule(x)
	if e != nil {
		t.Fatal(e)
	}
	s.mu.Lock()
	first := s.makeRun(x, time.Now())
	first.State = "running"
	second := s.makeRun(x, time.Now())
	s.state.Runs = []Run{first, second}
	s.mu.Unlock()
	s.tick(time.Now())
	if s.Snapshot().Runs[1].State != "queued" {
		t.Fatal("persistent session ran concurrently")
	}
	cfg := s.Snapshot().Settings
	cfg.GlobalParallel = 1
	s.SetSettings(cfg)
	s.mu.Lock()
	s.state.Runs[1].SessionID = "different"
	s.mu.Unlock()
	s.tick(time.Now())
	if s.Snapshot().Runs[1].State != "queued" {
		t.Fatal("global concurrency exceeded")
	}
	cfg.GlobalParallel = 4
	s.SetSettings(cfg)
	a.MaxParallel = 1
	s.SaveAgent(a)
	s.tick(time.Now())
	if s.Snapshot().Runs[1].State != "queued" {
		t.Fatal("agent concurrency exceeded")
	}
}
func TestSampleSurvivesSaveAndRestart(t *testing.T) {
	s := service(t)
	a := agent(t, s)
	x := schedule(t, s, a)
	x.Trigger = Trigger{Kind: "random_interval", MinSeconds: 72000, MaxSeconds: 108000}
	x, e := s.SaveSchedule(x)
	if e != nil {
		t.Fatal(e)
	}
	original := x.NextRun
	x.Task = "edited task"
	x, e = s.SaveSchedule(x)
	if e != nil || !x.NextRun.Equal(original) {
		t.Fatal("edit resampled next run")
	}
	other, e := New(s.Home())
	if e != nil {
		t.Fatal(e)
	}
	defer other.Close()
	if !other.Snapshot().Schedules[0].NextRun.Equal(original) {
		t.Fatal("restart resampled next run")
	}
}
