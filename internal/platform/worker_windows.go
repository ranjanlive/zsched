//go:build windows

package platform

import (
	"fmt"
	"os/exec"
	"sync"
	"syscall"
	"unsafe"
)

type threadEntry struct {
	Size, Usage, ThreadID, OwnerProcessID uint32
	BasePriority, DeltaPriority           int32
	Flags                                 uint32
}

// StartWorker leaves the initial thread suspended until job assignment and the
// caller's durable start record are complete. Failed setup reaps the child.
func StartWorker(cmd *exec.Cmd) (release func(), resume func() error, err error) {
	Configure(cmd)
	cmd.SysProcAttr.CreationFlags |= 0x00000004 // CREATE_SUSPENDED
	if err = cmd.Start(); err != nil {
		return nil, nil, err
	}
	release, err = Track(cmd)
	if err != nil {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
		return nil, nil, fmt.Errorf("assign worker job: %w", err)
	}
	snapshot, err := syscall.CreateToolhelp32Snapshot(0x00000004, 0) // TH32CS_SNAPTHREAD
	if err != nil {
		release()
		_ = cmd.Wait()
		return nil, nil, err
	}
	defer syscall.CloseHandle(snapshot)
	entry := threadEntry{Size: uint32(unsafe.Sizeof(threadEntry{}))}
	first := kernel.NewProc("Thread32First")
	next := kernel.NewProc("Thread32Next")
	ok, _, scanErr := first.Call(uintptr(snapshot), uintptr(unsafe.Pointer(&entry)))
	var thread syscall.Handle
	for ok != 0 {
		if entry.OwnerProcessID == uint32(cmd.Process.Pid) {
			h, _, openErr := kernel.NewProc("OpenThread").Call(0x0002, 0, uintptr(entry.ThreadID)) // THREAD_SUSPEND_RESUME
			if h == 0 {
				err = openErr
			} else {
				thread = syscall.Handle(h)
			}
			break
		}
		entry.Size = uint32(unsafe.Sizeof(entry))
		ok, _, scanErr = next.Call(uintptr(snapshot), uintptr(unsafe.Pointer(&entry)))
	}
	if thread == 0 {
		release()
		_ = cmd.Wait()
		if err == nil {
			err = fmt.Errorf("cannot locate suspended worker thread: %v", scanErr)
		}
		return nil, nil, err
	}
	jobRelease := release
	var once sync.Once
	release = func() { once.Do(func() { _ = syscall.CloseHandle(thread); jobRelease() }) }
	var resumed bool
	var resumeMu sync.Mutex
	resume = func() error {
		resumeMu.Lock()
		defer resumeMu.Unlock()
		if resumed {
			return nil
		}
		v, _, e := kernel.NewProc("ResumeThread").Call(uintptr(thread))
		if uint32(v) == 0xffffffff {
			return fmt.Errorf("resume worker thread: %w", e)
		}
		resumed = true
		return nil
	}
	return release, resume, nil
}
