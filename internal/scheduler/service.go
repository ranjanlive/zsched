package scheduler

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
	"zsched/internal/platform"
)

type Service struct {
	mu             sync.Mutex
	home           string
	state          State
	wake           chan struct{}
	active         map[string]context.CancelFunc
	wg             sync.WaitGroup
	ctx            context.Context
	cancel         context.CancelFunc
	OnNotification func(Notification)
	ExecuteBuiltin func(context.Context, Run) (string, error)
	lastError      string
}

func New(home string) (*Service, error) {
	home, e := filepath.Abs(home)
	if e != nil {
		return nil, e
	}
	if e = os.MkdirAll(filepath.Join(home, "runs"), 0700); e != nil {
		return nil, e
	}
	st, e := readState(home)
	if e != nil {
		return nil, e
	}
	ctx, cancel := context.WithCancel(context.Background())
	s := &Service{home: home, state: st, wake: make(chan struct{}, 1), active: map[string]context.CancelFunc{}, ctx: ctx, cancel: cancel}
	// Never retry an uncertain side effect automatically after a crash.
	for i := range s.state.Runs {
		x := &s.state.Runs[i]
		if x.State == "running" || x.State == "starting" {
			x.State = "interrupted"
			x.EndedAt = time.Now().UTC()
			x.Error = "The scheduler stopped during this run. Inspect its log before retrying."
		}
	}
	if e = s.persist(); e != nil {
		cancel()
		return nil, e
	}
	return s, nil
}
func (s *Service) Home() string { return s.home }
func (s *Service) Snapshot() State {
	s.mu.Lock()
	defer s.mu.Unlock()
	b, _ := json.Marshal(s.state)
	var cp State
	json.Unmarshal(b, &cp)
	return cp
}
func (s *Service) signal() {
	select {
	case s.wake <- struct{}{}:
	default:
	}
}

// transaction restores memory when durable persistence fails.
func (s *Service) transaction(action, target string, fn func() error) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	before, _ := json.Marshal(s.state)
	if e := fn(); e != nil {
		json.Unmarshal(before, &s.state)
		return e
	}
	s.audit(action, target)
	if e := s.persist(); e != nil {
		json.Unmarshal(before, &s.state)
		return e
	}
	s.signal()
	return nil
}
func (s *Service) Start()         { s.wg.Add(1); go s.loop() }
func (s *Service) Close()         { s.cancel(); s.signal(); s.wg.Wait() }
func (s *Service) Health() string { s.mu.Lock(); defer s.mu.Unlock(); return s.lastError }
func (s *Service) loop() {
	defer s.wg.Done()
	for {
		s.tick(time.Now().UTC())
		timer := time.NewTimer(s.delay())
		select {
		case <-s.ctx.Done():
			timer.Stop()
			return
		case <-s.wake:
			timer.Stop()
		case <-timer.C:
		}
	}
}
func (s *Service) delay() time.Duration {
	s.mu.Lock()
	defer s.mu.Unlock()
	d := time.Minute
	if s.state.Settings.Paused {
		return d
	}
	now := time.Now()
	for _, x := range s.state.Schedules {
		a := s.agent(x.AgentID)
		if x.Enabled && a != nil && a.Enabled && !x.NextRun.IsZero() {
			v := x.NextRun.Sub(now)
			if v < d {
				d = v
			}
		}
	}
	for _, x := range s.state.Runs {
		schedule := s.schedule(x.ScheduleID)
		if x.State == "queued" && x.ScheduledAt.After(now) && (schedule == nil || !schedule.Paused || x.Manual) {
			v := x.ScheduledAt.Sub(now)
			if v < d {
				d = v
			}
		}
	}
	if d < time.Second {
		d = time.Second
	}
	return d
}
func (s *Service) agent(id string) *Agent {
	for i := range s.state.Agents {
		if s.state.Agents[i].ID == id {
			return &s.state.Agents[i]
		}
	}
	return nil
}
func (s *Service) schedule(id string) *Schedule {
	for i := range s.state.Schedules {
		if s.state.Schedules[i].ID == id {
			return &s.state.Schedules[i]
		}
	}
	return nil
}
func (s *Service) run(id string) *Run {
	for i := range s.state.Runs {
		if s.state.Runs[i].ID == id {
			return &s.state.Runs[i]
		}
	}
	return nil
}
func (s *Service) pending(id string) bool {
	for _, r := range s.state.Runs {
		if r.ScheduleID == id && (r.State == "queued" || r.State == "running" || r.State == "starting") {
			return true
		}
	}
	return false
}
func (s *Service) makeRun(x Schedule, due time.Time) Run {
	a := *s.agent(x.AgentID)
	rid := id("r-")
	sid := "zsched-" + rid
	if x.SessionMode == "persistent" {
		sid = "zsched-" + x.ID
	}
	return Run{ID: rid, ScheduleID: x.ID, ScheduleName: x.Name, AgentID: a.ID, AgentName: a.Name, SessionID: sid, Agent: a, Task: x.Task, State: "queued", ScheduledAt: due, Attempt: 1, TimeoutMinutes: x.TimeoutMinutes, RetryCount: x.RetryCount, RetryDelaySeconds: x.RetryDelaySeconds, LogPath: filepath.Join(s.home, "runs", rid, "output.log"), ExitCode: -1}
}
func (s *Service) tick(now time.Time) {
	s.mu.Lock()
	if s.ctx.Err() != nil || s.state.Settings.Paused {
		s.mu.Unlock()
		return
	}
	before, _ := json.Marshal(s.state)
	changed := false
	for i := range s.state.Schedules {
		x := &s.state.Schedules[i]
		a := s.agent(x.AgentID)
		if !x.Enabled || a == nil || !a.Enabled || x.NextRun.IsZero() || x.NextRun.After(now) {
			continue
		}
		due := x.NextRun
		late := now.Sub(due) > time.Minute
		if !(late && x.Missed == "skip") && (!s.pending(x.ID) || x.Overlap == "queue") {
			// At most one queued occurrence per schedule, avoiding unbounded backlogs.
			queued := false
			for _, v := range s.state.Runs {
				if v.ScheduleID == x.ID && v.State == "queued" {
					queued = true
				}
			}
			if !queued {
				s.state.Runs = append(s.state.Runs, s.makeRun(*x, due))
			}
		}
		x.LastRun = due
		if x.Trigger.Kind == "once" {
			x.NextRun = time.Time{}
			x.Enabled = false
		} else {
			x.NextRun, _ = Next(x.Trigger, now, nil)
		}
		changed = true
	}
	count := map[string]int{}
	total := 0
	for _, r := range s.state.Runs {
		if r.State == "running" || r.State == "starting" {
			count[r.AgentID]++
			total++
		}
	}
	var launch []Run
	for i := range s.state.Runs {
		r := &s.state.Runs[i]
		a := s.agent(r.AgentID)
		x := s.schedule(r.ScheduleID)
		if x != nil && x.Paused && !r.Manual {
			continue
		}
		if r.State != "queued" || r.ScheduledAt.After(now) || a == nil || !a.Enabled || total >= s.state.Settings.GlobalParallel || count[r.AgentID] >= a.MaxParallel {
			continue
		}
		sameSession := false
		for _, v := range s.state.Runs {
			if (v.State == "running" || v.State == "starting") && v.SessionID == r.SessionID {
				sameSession = true
			}
		}
		if sameSession {
			continue
		}
		r.State = "starting"
		r.StartedAt = now
		launch = append(launch, *r)
		count[r.AgentID]++
		total++
		changed = true
	}
	if changed {
		if e := s.persist(); e != nil {
			json.Unmarshal(before, &s.state)
			s.lastError = "Cannot persist scheduler state: " + e.Error()
			s.mu.Unlock()
			return
		}
		s.lastError = ""
	}
	for _, r := range launch {
		ctx, cancel := context.WithCancel(s.ctx)
		s.active[r.ID] = cancel
		s.wg.Add(1)
		go s.execute(ctx, r)
	}
	s.mu.Unlock()
}
func validateAgent(a *Agent) error {
	a.Name = strings.TrimSpace(a.Name)
	if a.Name == "" || len(a.Name) > 100 {
		return errors.New("agent name is required (maximum 100 characters)")
	}
	if a.Binary == "builtin:assistant" {
		if a.Home == "" {
			return errors.New("built-in agent home is required")
		}
		var e error
		a.Home, e = filepath.Abs(a.Home)
		if e != nil {
			return e
		}
		if a.Workspace == "" {
			a.Workspace = filepath.Join(a.Home, "workspace")
		}
		if a.MaxParallel == 0 {
			a.MaxParallel = 1
		}
		if a.MaxParallel < 1 || a.MaxParallel > 64 {
			return errors.New("parallel runs must be 1-64")
		}
		a.Workspace, e = filepath.Abs(a.Workspace)
		if e != nil {
			return e
		}
		if e = os.MkdirAll(a.Home, 0700); e != nil {
			return e
		}
		return os.MkdirAll(a.Workspace, 0700)
	}
	p, e := filepath.Abs(a.Binary)
	if e != nil {
		return e
	}
	st, e := os.Stat(p)
	if e != nil || st.IsDir() {
		return errors.New("choose an existing agent executable")
	}
	a.Binary = p
	if a.Home == "" {
		a.Home = filepath.Dir(p)
	}
	a.Home, e = filepath.Abs(a.Home)
	if e != nil {
		return e
	}
	if a.Workspace == "" {
		a.Workspace = filepath.Join(a.Home, "workspace")
	}
	a.Workspace, e = filepath.Abs(a.Workspace)
	if e != nil {
		return e
	}
	if a.MaxParallel == 0 {
		a.MaxParallel = 1
	}
	if a.MaxParallel < 1 || a.MaxParallel > 64 {
		return errors.New("parallel runs must be between 1 and 64")
	}
	if e = os.MkdirAll(a.Home, 0700); e != nil {
		return e
	}
	return os.MkdirAll(a.Workspace, 0755)
}
func (s *Service) SaveAgent(a Agent) (Agent, error) {
	if a.Binary == "builtin:assistant" && a.Home == "" {
		a.Home = filepath.Join(s.home, "assistant")
	}
	if e := validateAgent(&a); e != nil {
		return a, e
	}
	e := s.transaction("save_agent", a.ID, func() error {
		for _, v := range s.state.Agents {
			if strings.EqualFold(v.Name, a.Name) && v.ID != a.ID {
				return errors.New("an agent with that name already exists")
			}
		}
		if a.ID == "" {
			a.ID = id("a-")
			a.Enabled = true
			s.state.Agents = append(s.state.Agents, a)
		} else {
			v := s.agent(a.ID)
			if v == nil {
				return errors.New("agent not found")
			}
			*v = a
		}
		return nil
	})
	return a, e
}
func (s *Service) DeleteAgent(aid string) error {
	return s.transaction("delete_agent", aid, func() error {
		if s.agent(aid) == nil {
			return errors.New("agent not found")
		}
		for _, x := range s.state.Schedules {
			if x.AgentID == aid {
				return errors.New("remove this agent's schedules first")
			}
		}
		for _, r := range s.state.Runs {
			if r.AgentID == aid && (r.State == "queued" || r.State == "running" || r.State == "starting") {
				return errors.New("stop or cancel this agent's runs first")
			}
		}
		for i, a := range s.state.Agents {
			if a.ID == aid {
				s.state.Agents = append(s.state.Agents[:i], s.state.Agents[i+1:]...)
				break
			}
		}
		return nil
	})
}
func (s *Service) SaveSchedule(x Schedule) (Schedule, error) {
	x.Name = strings.TrimSpace(x.Name)
	if x.Name == "" || len(x.Name) > 140 {
		return x, errors.New("schedule name is required (maximum 140 characters)")
	}
	if strings.TrimSpace(x.Task) == "" || len(x.Task) > 1024*1024 {
		return x, errors.New("task is required (maximum 1 MB)")
	}
	if x.Trigger.Timezone == "" {
		x.Trigger.Timezone = "Local"
	}
	if e := x.Trigger.Validate(); e != nil {
		return x, e
	}
	if x.Overlap == "" {
		x.Overlap = "skip"
	}
	if x.Overlap != "skip" && x.Overlap != "queue" {
		return x, errors.New("overlap must be skip or queue")
	}
	if x.Missed == "" {
		x.Missed = "run_once"
	}
	if x.Missed != "skip" && x.Missed != "run_once" {
		return x, errors.New("missed runs must be skip or run_once")
	}
	if x.SessionMode == "" {
		x.SessionMode = "fresh"
	}
	if x.SessionMode != "fresh" && x.SessionMode != "persistent" {
		return x, errors.New("session mode must be fresh or persistent")
	}
	if x.TimeoutMinutes == 0 {
		x.TimeoutMinutes = 60
	}
	if x.TimeoutMinutes < 1 || x.TimeoutMinutes > 10080 || x.RetryCount < 0 || x.RetryCount > 5 {
		return x, errors.New("timeout must be 1–10080 minutes; retries 0–5")
	}
	if x.RetryDelaySeconds == 0 {
		x.RetryDelaySeconds = 300
	}
	if x.RetryDelaySeconds < 1 {
		return x, errors.New("retry delay must be positive")
	}
	e := s.transaction("save_schedule", x.ID, func() error {
		if s.agent(x.AgentID) == nil {
			return errors.New("agent not found")
		}
		var old *Schedule
		if x.ID != "" {
			old = s.schedule(x.ID)
			if old == nil {
				return errors.New("schedule not found")
			}
		}
		next, e := Next(x.Trigger, time.Now(), nil)
		if e != nil {
			return e
		}
		if x.Trigger.Kind == "once" && next.IsZero() {
			return errors.New("one-time schedule must be in the future")
		}
		if old != nil {
			a, _ := json.Marshal(old.Trigger)
			b, _ := json.Marshal(x.Trigger)
			if string(a) == string(b) && !old.NextRun.IsZero() {
				next = old.NextRun
			}
			x.CreatedAt = old.CreatedAt
			x.LastRun = old.LastRun
		} else {
			x.ID = id("s-")
			x.CreatedAt = time.Now().UTC()
			x.Enabled = true
		}
		x.NextRun = next
		x.Paused = !x.Enabled
		if old != nil {
			*old = x
		} else {
			s.state.Schedules = append(s.state.Schedules, x)
		}
		return nil
	})
	return x, e
}
func (s *Service) PauseSchedule(sid string, paused bool) error {
	return s.transaction("pause_schedule", sid, func() error {
		x := s.schedule(sid)
		if x == nil {
			return errors.New("schedule not found")
		}
		if !paused && x.NextRun.IsZero() {
			n, e := Next(x.Trigger, time.Now(), nil)
			if e != nil {
				return e
			}
			if n.IsZero() {
				return errors.New("this one-time schedule has completed; edit its date to run again")
			}
			x.NextRun = n
		}
		x.Enabled = !paused
		x.Paused = paused
		return nil
	})
}
func (s *Service) DeleteSchedule(sid string) error {
	return s.transaction("delete_schedule", sid, func() error {
		if s.schedule(sid) == nil {
			return errors.New("schedule not found")
		}
		if s.pending(sid) {
			return errors.New("stop or cancel this schedule's runs before deleting it")
		}
		for i, x := range s.state.Schedules {
			if x.ID == sid {
				s.state.Schedules = append(s.state.Schedules[:i], s.state.Schedules[i+1:]...)
				break
			}
		}
		return nil
	})
}
func (s *Service) RunNow(sid string) (Run, error) {
	var r Run
	e := s.transaction("run_now", sid, func() error {
		x := s.schedule(sid)
		if x == nil {
			return errors.New("schedule not found")
		}
		if s.pending(sid) && x.Overlap == "skip" {
			return errors.New("this schedule already has a pending run")
		}
		r = s.makeRun(*x, time.Now().UTC())
		r.Manual = true
		s.state.Runs = append(s.state.Runs, r)
		return nil
	})
	return r, e
}
func (s *Service) StopRun(rid string) error {
	s.mu.Lock()
	c := s.active[rid]
	s.mu.Unlock()
	if c != nil {
		c()
		return nil
	}
	return s.transaction("cancel_run", rid, func() error {
		r := s.run(rid)
		if r == nil {
			return errors.New("run not found")
		}
		if r.State != "queued" {
			return errors.New("run has already finished")
		}
		r.State = "cancelled"
		r.EndedAt = time.Now().UTC()
		return nil
	})
}
func (s *Service) StopAll() error {
	st := s.Snapshot()
	var errs []error
	for _, r := range st.Runs {
		if r.State == "running" || r.State == "starting" || r.State == "queued" {
			if err := s.StopRun(r.ID); err != nil {
				errs = append(errs, err)
			}
		}
	}
	return errors.Join(errs...)
}
func (s *Service) SetSettings(v Settings) error {
	if v.GlobalParallel < 1 || v.GlobalParallel > 64 {
		return errors.New("global parallel runs must be 1–64")
	}
	if v.HistoryLimit < 20 || v.HistoryLimit > 10000 {
		return errors.New("history limit must be 20–10000")
	}
	if v.WebPort < 0 || v.WebPort > 65535 {
		return errors.New("invalid web port")
	}
	if v.AI.ContextLimit < 1000 || v.AI.ContextLimit > 2000000 || v.AI.CompactAt < 500 || v.AI.CompactAt >= v.AI.ContextLimit {
		return errors.New("assistant compaction threshold must be >= 500 and below its context limit")
	}
	return s.transaction("settings", "", func() error { v.AI.KeySet = s.state.Settings.AI.KeySet; s.state.Settings = v; return nil })
}
func (s *Service) Notify(title, body, kind string) {
	n := Notification{id("n-"), time.Now().UTC(), title, body, kind, false}
	var popup bool
	e := s.transaction("notification", n.ID, func() error {
		s.state.Notifications = append(s.state.Notifications, n)
		if len(s.state.Notifications) > 200 {
			s.state.Notifications = s.state.Notifications[len(s.state.Notifications)-200:]
		}
		popup = s.state.Settings.Notifications && !s.state.Settings.Quiet
		return nil
	})
	if e == nil && popup && s.OnNotification != nil {
		s.OnNotification(n)
	}
}
func (s *Service) ReadNotifications() error {
	return s.transaction("read_notifications", "", func() error {
		for i := range s.state.Notifications {
			s.state.Notifications[i].Read = true
		}
		return nil
	})
}
func (s *Service) ClearHistory() error {
	return s.transaction("clear_history", "", func() error {
		keep := []Run{}
		for _, r := range s.state.Runs {
			if r.State == "running" || r.State == "starting" || r.State == "queued" {
				keep = append(keep, r)
			}
		}
		s.state.Runs = keep
		return nil
	})
}
func (s *Service) SetKey(key string) error {
	b, e := platform.Protect([]byte(key))
	if e != nil {
		return e
	}
	if e = Atomic(filepath.Join(s.home, "assistant.key"), b); e != nil {
		return e
	}
	return s.transaction("assistant_key", "", func() error { s.state.Settings.AI.KeySet = key != ""; return nil })
}
func (s *Service) Key() (string, error) {
	b, e := os.ReadFile(filepath.Join(s.home, "assistant.key"))
	if os.IsNotExist(e) {
		return "", nil
	}
	if e != nil {
		return "", e
	}
	b, e = platform.Unprotect(b)
	return string(b), e
}
func (s *Service) RunLog(rid string) (string, error) {
	s.mu.Lock()
	r := s.run(rid)
	if r == nil {
		s.mu.Unlock()
		return "", errors.New("run not found")
	}
	p := r.LogPath
	s.mu.Unlock()
	f, e := os.Open(p)
	if os.IsNotExist(e) {
		return "Run has not produced output yet.", nil
	}
	if e != nil {
		return "", e
	}
	defer f.Close()
	st, e := f.Stat()
	if e != nil {
		return "", e
	}
	start := max(int64(0), st.Size()-128*1024)
	f.Seek(start, 0)
	b := make([]byte, 128*1024)
	n, _ := f.Read(b)
	return strings.ToValidUTF8(string(b[:n]), "�"), nil
}
func (s *Service) OpenAgent(aid, rid, ui string) error {
	if ui != "web" && ui != "cli" {
		return errors.New("interface must be web or cli")
	}
	st := s.Snapshot()
	var a Agent
	sid := "zsched-interactive-" + aid
	if rid != "" {
		for _, r := range st.Runs {
			if r.ID == rid {
				a = r.Agent
				sid = r.SessionID
				break
			}
		}
	} else {
		for _, v := range st.Agents {
			if v.ID == aid {
				a = v
				break
			}
		}
	}
	if a.Binary == "builtin:assistant" {
		return errors.New("open the zsched Assistant tab for this built-in agent")
	}
	if a.Binary == "" {
		return errors.New("agent or run not found")
	}
	args := []string{"--home", a.Home, "--workspace", a.Workspace, "--session", sid, "--ui", ui}
	if ui == "cli" {
		return platform.OpenTerminal(a.Binary, args)
	}
	cmd := exec.Command(a.Binary, args...)
	platform.Configure(cmd)
	if e := cmd.Start(); e != nil {
		return e
	}
	go cmd.Wait()
	return nil
}
func (s *Service) trim() {
	limit := s.state.Settings.HistoryLimit
	if len(s.state.Runs) <= limit {
		return
	}
	sort.SliceStable(s.state.Runs, func(i, j int) bool { return s.state.Runs[i].ScheduledAt.Before(s.state.Runs[j].ScheduledAt) })
	extra := len(s.state.Runs) - limit
	out := []Run{}
	for _, r := range s.state.Runs {
		if extra > 0 && r.State != "running" && r.State != "starting" && r.State != "queued" {
			extra--
			continue
		}
		out = append(out, r)
	}
	s.state.Runs = out
}
