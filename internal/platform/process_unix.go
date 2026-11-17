//go:build !windows

package platform

import (
	"fmt"
	"os"
	"os/exec"
	"syscall"
)

type Lock struct{ f *os.File }

func Acquire(path string) (*Lock, error) {
	f, e := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0600)
	if e != nil {
		return nil, e
	}
	if e = syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); e != nil {
		f.Close()
		return nil, fmt.Errorf("scheduler is already running: %w", e)
	}
	return &Lock{f}, nil
}
func (l *Lock) Close()                    { l.f.Close() }
func Replace(from, to string) error       { return os.Rename(from, to) }
func Configure(cmd *exec.Cmd)             { cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true} }
func Track(cmd *exec.Cmd) (func(), error) { return func() {}, nil }
func Kill(cmd *exec.Cmd) {
	if cmd.Process != nil {
		syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
	}
}
func OpenURL(url string) error {
	var c *exec.Cmd
	if _, e := exec.LookPath("xdg-open"); e == nil {
		c = exec.Command("xdg-open", url)
	} else {
		c = exec.Command("open", url)
	}
	if e := c.Start(); e != nil {
		return e
	}
	go c.Wait()
	return nil
}
func Startup(exe, home string, on bool) error {
	return fmt.Errorf("automatic login startup is currently supported on Windows; configure your OS to launch zsched daemon --home %s", home)
}
func Protect(b []byte) ([]byte, error)   { return b, nil }
func Unprotect(b []byte) ([]byte, error) { return b, nil }

func OpenTerminal(exe string, args []string) error {
	if _, e := exec.LookPath("x-terminal-emulator"); e == nil {
		c := exec.Command("x-terminal-emulator", append([]string{"-e", exe}, args...)...)
		if e = c.Start(); e != nil {
			return e
		}
		go c.Wait()
		return nil
	}
	return fmt.Errorf("open a terminal and run %s with --ui cli", exe)
}
