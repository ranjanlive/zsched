package platform

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"testing"
	"time"
)

func TestWorkerHelper(t *testing.T) {
	marker := os.Getenv("ZSCHED_WORKER_TEST_MARKER")
	if marker == "" {
		t.Skip("helper process")
	}
	if e := os.WriteFile(marker, []byte("ran"), 0600); e != nil {
		os.Exit(2)
	}
	if os.Getenv("ZSCHED_WORKER_TEST_WAIT") == "1" {
		time.Sleep(time.Hour)
	}
	os.Exit(0)
}

func TestSupervisedImmediateExit(t *testing.T) {
	for range 5 {
		marker := filepath.Join(t.TempDir(), "ran")
		cmd := exec.Command(os.Args[0], "-test.run=^TestWorkerHelper$")
		cmd.Env = append(os.Environ(), "ZSCHED_WORKER_TEST_MARKER="+marker)
		release, resume, e := StartWorker(cmd)
		if e != nil {
			t.Fatal(e)
		}
		if runtime.GOOS == "windows" {
			if _, e = os.Stat(marker); !os.IsNotExist(e) {
				release()
				_ = cmd.Wait()
				t.Fatal("worker ran before supervision/start commit")
			}
		}
		if e = resume(); e != nil {
			release()
			_ = cmd.Wait()
			t.Fatal(e)
		}
		done := make(chan error, 1)
		go func() { done <- cmd.Wait() }()
		select {
		case e = <-done:
		case <-time.After(15 * time.Second):
			Kill(cmd)
			e = <-done
			t.Fatal("worker did not exit")
		}
		release()
		if e != nil {
			t.Fatal(e)
		}
		if b, e := os.ReadFile(marker); e != nil || string(b) != "ran" {
			t.Fatal("worker did not execute")
		}
	}
}

func TestWindowsJobCloseTerminatesWorker(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("Windows job containment")
	}
	marker := filepath.Join(t.TempDir(), "ran")
	cmd := exec.Command(os.Args[0], "-test.run=^TestWorkerHelper$")
	cmd.Env = append(os.Environ(), "ZSCHED_WORKER_TEST_MARKER="+marker, "ZSCHED_WORKER_TEST_WAIT=1")
	release, resume, e := StartWorker(cmd)
	if e != nil {
		t.Fatal(e)
	}
	defer release()
	if e = resume(); e != nil {
		t.Fatal(e)
	}
	deadline := time.Now().Add(15 * time.Second)
	for {
		if _, e := os.Stat(marker); e == nil {
			break
		}
		if time.Now().After(deadline) {
			Kill(cmd)
			_ = cmd.Wait()
			t.Fatal("worker did not start")
		}
		time.Sleep(10 * time.Millisecond)
	}
	release()
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	select {
	case <-done:
	case <-time.After(15 * time.Second):
		Kill(cmd)
		<-done
		t.Fatal("job close left worker alive")
	}
}
