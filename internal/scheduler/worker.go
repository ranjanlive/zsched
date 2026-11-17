package scheduler

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"time"
	"zsched/internal/platform"
)

type cappedLog struct {
	mu        sync.Mutex
	f         *os.File
	n         int
	truncated bool
}

func (w *cappedLog) Write(b []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	original := len(b)
	left := 8*1024*1024 - w.n
	if left <= 0 {
		if !w.truncated {
			w.f.WriteString("\n[zsched: log limit reached; subsequent output omitted]\n")
			w.truncated = true
		}
		return original, nil
	}
	if len(b) > left {
		b = b[:left]
	}
	n, e := w.f.Write(b)
	w.n += n
	if e != nil {
		return n, e
	}
	return original, nil
}
func (s *Service) execute(parent context.Context, r Run) {
	defer s.wg.Done()
	ctx, cancel := context.WithTimeout(parent, time.Duration(r.TimeoutMinutes)*time.Minute)
	defer cancel()
	dir := filepath.Dir(r.LogPath)
	var err error
	exit := -1
	if err = os.MkdirAll(dir, 0700); err == nil {
		err = Atomic(filepath.Join(dir, "task.txt"), []byte(r.Task))
	}
	var log *os.File
	if err == nil {
		log, err = os.OpenFile(r.LogPath, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0600)
	}
	if err == nil && r.Agent.Binary == "builtin:assistant" {
		defer log.Close()
		err = s.transaction("worker_started", r.ID, func() error {
			if v := s.run(r.ID); v != nil {
				v.State = "running"
			}
			return nil
		})
		if err == nil {
			if s.ExecuteBuiltin == nil {
				err = fmt.Errorf("built-in assistant is unavailable")
			} else {
				var output string
				output, err = s.ExecuteBuiltin(ctx, r)
				_, _ = (&cappedLog{f: log}).Write([]byte(output))
				if err == nil {
					exit = 0
				}
			}
		}
	} else if err == nil {
		defer log.Close()
		writer := &cappedLog{f: log}
		cmd := exec.Command(r.Agent.Binary, "--home", r.Agent.Home, "--workspace", r.Agent.Workspace, "--session", r.SessionID, "--prompt-file", filepath.Join(dir, "task.txt"), "--verbose")
		cmd.Dir = r.Agent.Workspace
		cmd.Stdout = writer
		cmd.Stderr = writer
		cmd.Env = os.Environ()
		var release func()
		var resume func() error
		if ctx.Err() != nil {
			err = ctx.Err()
		} else {
			release, resume, err = platform.StartWorker(cmd)
		}
		if err == nil {
			persistErr := s.transaction("worker_started", r.ID, func() error {
				if v := s.run(r.ID); v != nil {
					v.State = "running"
					v.PID = cmd.Process.Pid
				}
				return nil
			})
			if persistErr == nil {
				persistErr = ctx.Err()
			}
			if persistErr == nil {
				persistErr = resume()
			}
			if persistErr != nil {
				platform.Kill(cmd)
			}
			done := make(chan error, 1)
			go func() { done <- cmd.Wait() }()
			select {
			case err = <-done:
			case <-ctx.Done():
				platform.Kill(cmd)
				err = <-done
				if err == nil {
					err = ctx.Err()
				}
			}
			release()
			if persistErr != nil {
				err = fmt.Errorf("cannot commit or resume worker start: %w", persistErr)
			}
			if cmd.ProcessState != nil {
				exit = cmd.ProcessState.ExitCode()
			}
		}
	}
	state := "succeeded"
	detail := ""
	if err != nil {
		state = "failed"
		detail = err.Error()
	}
	if ctx.Err() == context.DeadlineExceeded {
		state = "timed_out"
		detail = "Run exceeded its runtime limit"
	} else if parent.Err() != nil {
		state = "cancelled"
		detail = "Run was stopped"
	}
	e := s.transaction("worker_finished", r.ID, func() error {
		v := s.run(r.ID)
		if v == nil {
			return nil
		}
		v.State = state
		v.ExitCode = exit
		v.EndedAt = time.Now().UTC()
		v.Error = detail
		if state == "failed" && r.Attempt <= r.RetryCount {
			retry := r
			retry.ID = id("r-")
			if x := s.schedule(r.ScheduleID); x == nil || x.SessionMode != "persistent" {
				retry.SessionID = "zsched-" + retry.ID
			}
			retry.Attempt++
			retry.State = "queued"
			retry.ScheduledAt = time.Now().Add(time.Duration(r.RetryDelaySeconds) * time.Second).UTC()
			retry.StartedAt = time.Time{}
			retry.EndedAt = time.Time{}
			retry.PID = 0
			retry.LogPath = filepath.Join(s.home, "runs", retry.ID, "output.log")
			s.state.Runs = append(s.state.Runs, retry)
		}
		s.trim()
		return nil
	})
	s.mu.Lock()
	delete(s.active, r.ID)
	if e != nil {
		s.lastError = "Cannot save run result: " + e.Error()
	}
	s.mu.Unlock()
	s.signal()
	kind := "success"
	if state != "succeeded" {
		kind = "error"
	}
	s.Notify(r.ScheduleName+" · "+state, fmt.Sprintf("%s • attempt %d", r.AgentName, r.Attempt), kind)
}
