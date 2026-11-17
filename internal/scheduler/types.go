package scheduler

import "time"

const Version = "0.1.0"

type Agent struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
	Binary      string `json:"binary"`
	Home        string `json:"home"`
	Workspace   string `json:"workspace"`
	MaxParallel int    `json:"max_parallel_run"`
	Enabled     bool   `json:"enabled"`
}
type Trigger struct {
	Kind         string    `json:"kind"`
	At           time.Time `json:"at,omitempty"`
	EverySeconds int64     `json:"every_seconds,omitempty"`
	MinSeconds   int64     `json:"min_seconds,omitempty"`
	MaxSeconds   int64     `json:"max_seconds,omitempty"`
	Days         []int     `json:"days,omitempty"`
	Time         string    `json:"time,omitempty"`
	WindowStart  string    `json:"window_start,omitempty"`
	WindowEnd    string    `json:"window_end,omitempty"`
	Timezone     string    `json:"timezone"`
}
type Schedule struct {
	Paused            bool      `json:"paused"`
	ID                string    `json:"id"`
	Name              string    `json:"name"`
	AgentID           string    `json:"agent_id"`
	Task              string    `json:"task"`
	Trigger           Trigger   `json:"trigger"`
	Enabled           bool      `json:"enabled"`
	NextRun           time.Time `json:"next_run,omitempty"`
	LastRun           time.Time `json:"last_run,omitempty"`
	Overlap           string    `json:"overlap"`
	Missed            string    `json:"missed_runs"`
	TimeoutMinutes    int       `json:"timeout_minutes"`
	RetryCount        int       `json:"retry_count"`
	RetryDelaySeconds int       `json:"retry_delay_seconds"`
	SessionMode       string    `json:"session_mode"`
	CreatedAt         time.Time `json:"created_at"`
}
type Run struct {
	Manual            bool      `json:"manual"`
	ID                string    `json:"id"`
	ScheduleID        string    `json:"schedule_id"`
	ScheduleName      string    `json:"schedule_name"`
	AgentID           string    `json:"agent_id"`
	AgentName         string    `json:"agent_name"`
	SessionID         string    `json:"session_id"`
	Agent             Agent     `json:"agent"`
	Task              string    `json:"task"`
	State             string    `json:"state"`
	ScheduledAt       time.Time `json:"scheduled_at"`
	StartedAt         time.Time `json:"started_at,omitempty"`
	EndedAt           time.Time `json:"ended_at,omitempty"`
	ExitCode          int       `json:"exit_code"`
	Error             string    `json:"error,omitempty"`
	Attempt           int       `json:"attempt"`
	TimeoutMinutes    int       `json:"timeout_minutes"`
	RetryCount        int       `json:"retry_count"`
	RetryDelaySeconds int       `json:"retry_delay_seconds"`
	LogPath           string    `json:"log_path"`
	PID               int       `json:"pid,omitempty"`
}
type Notification struct {
	ID    string    `json:"id"`
	At    time.Time `json:"at"`
	Title string    `json:"title"`
	Body  string    `json:"body"`
	Kind  string    `json:"kind"`
	Read  bool      `json:"read"`
}
type AIConfig struct {
	URL          string `json:"url"`
	Model        string `json:"model"`
	ContextLimit int    `json:"context_limit"`
	CompactAt    int    `json:"compact_at"`
	KeySet       bool   `json:"key_set"`
}
type Settings struct {
	Paused         bool     `json:"paused"`
	Quiet          bool     `json:"quiet"`
	Notifications  bool     `json:"notifications"`
	GlobalParallel int      `json:"global_parallel"`
	HistoryLimit   int      `json:"history_limit"`
	WebEnabled     bool     `json:"web_enabled"`
	WebPort        int      `json:"web_port"`
	Startup        bool     `json:"startup"`
	AI             AIConfig `json:"ai"`
}
type Audit struct {
	At     time.Time `json:"at"`
	Action string    `json:"action"`
	Target string    `json:"target"`
}
type State struct {
	Version       int            `json:"version"`
	Agents        []Agent        `json:"agents"`
	Schedules     []Schedule     `json:"schedules"`
	Runs          []Run          `json:"runs"`
	Notifications []Notification `json:"notifications"`
	Audit         []Audit        `json:"audit"`
	Settings      Settings       `json:"settings"`
}

func defaults() State {
	return State{Version: 1, Agents: []Agent{}, Schedules: []Schedule{}, Runs: []Run{}, Notifications: []Notification{}, Audit: []Audit{}, Settings: Settings{Notifications: true, GlobalParallel: 4, HistoryLimit: 500, WebPort: 8787, AI: AIConfig{ContextLimit: 64000, CompactAt: 48000}}}
}
