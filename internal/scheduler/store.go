package scheduler

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"
	"zsched/internal/platform"
)

func id(prefix string) string {
	var b [12]byte
	if _, e := rand.Read(b[:]); e != nil {
		panic(e)
	}
	return prefix + hex.EncodeToString(b[:])
}
func readState(home string) (State, error) {
	s := defaults()
	b, e := os.ReadFile(filepath.Join(home, "state.json"))
	if os.IsNotExist(e) {
		return s, nil
	}
	if e != nil {
		return s, e
	}
	if e = json.Unmarshal(b, &s); e != nil {
		return s, fmt.Errorf("state.json is damaged; restore a backup: %w", e)
	}
	if s.Version != 1 {
		return s, fmt.Errorf("unsupported state version %d", s.Version)
	}
	return s, nil
}
func atomicJSON(path string, v any) error {
	b, e := json.MarshalIndent(v, "", "  ")
	if e != nil {
		return e
	}
	return Atomic(path, b)
}
func Atomic(path string, b []byte) error {
	if e := os.MkdirAll(filepath.Dir(path), 0700); e != nil {
		return e
	}
	f, e := os.CreateTemp(filepath.Dir(path), ".zsched-*")
	if e != nil {
		return e
	}
	tmp := f.Name()
	defer os.Remove(tmp)
	if e = f.Chmod(0600); e == nil {
		_, e = f.Write(b)
	}
	if e == nil {
		e = f.Sync()
	}
	ce := f.Close()
	if e == nil {
		e = ce
	}
	if e != nil {
		return e
	}
	return platform.Replace(tmp, path)
}
func (s *Service) persist() error { return atomicJSON(filepath.Join(s.home, "state.json"), s.state) }
func (s *Service) audit(action, target string) {
	s.state.Audit = append(s.state.Audit, Audit{time.Now().UTC(), action, target})
	if len(s.state.Audit) > 1000 {
		s.state.Audit = s.state.Audit[len(s.state.Audit)-1000:]
	}
}
