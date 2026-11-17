//go:build windows

package platform

import (
	"crypto/sha256"
	"fmt"
	"os"
	"os/exec"
	"syscall"
	"unsafe"
)

var kernel = syscall.NewLazyDLL("kernel32.dll")
var moveFile = kernel.NewProc("MoveFileExW")

type Lock struct{ h syscall.Handle }

func Acquire(path string) (*Lock, error) {
	p, e := syscall.UTF16PtrFromString(path)
	if e != nil {
		return nil, e
	}
	h, e := syscall.CreateFile(p, syscall.GENERIC_READ|syscall.GENERIC_WRITE, 0, nil, syscall.OPEN_ALWAYS, syscall.FILE_ATTRIBUTE_NORMAL, 0)
	if e != nil {
		return nil, fmt.Errorf("scheduler is already running or lock cannot be opened: %w", e)
	}
	return &Lock{h}, nil
}
func (l *Lock) Close() { syscall.CloseHandle(l.h) }
func Replace(from, to string) error {
	a, _ := syscall.UTF16PtrFromString(from)
	b, _ := syscall.UTF16PtrFromString(to)
	v, _, e := moveFile.Call(uintptr(unsafe.Pointer(a)), uintptr(unsafe.Pointer(b)), 0x1|0x8)
	if v == 0 {
		return e
	}
	return nil
}
func Configure(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true, CreationFlags: 0x08000000 | 0x00000200}
}

// Jobs are closed on daemon death, terminating workers and their descendants.
type jobBasic struct {
	PerProcess, PerJob   int64
	Flags                uint32
	Min, Max             uintptr
	Active               uint32
	Affinity             uintptr
	Priority, Scheduling uint32
}
type ioCounters struct{ Read, Write, Other, ReadBytes, WriteBytes, OtherBytes uint64 }
type jobExtended struct {
	Basic                                          jobBasic
	IO                                             ioCounters
	ProcessMemory, JobMemory, PeakProcess, PeakJob uintptr
}

func Track(cmd *exec.Cmd) (func(), error) {
	h, _, e := kernel.NewProc("CreateJobObjectW").Call(0, 0)
	if h == 0 {
		return nil, e
	}
	closeJob := func() { syscall.CloseHandle(syscall.Handle(h)) }
	info := jobExtended{}
	info.Basic.Flags = 0x2000
	v, _, e := kernel.NewProc("SetInformationJobObject").Call(h, 9, uintptr(unsafe.Pointer(&info)), unsafe.Sizeof(info))
	if v == 0 {
		closeJob()
		return nil, e
	}
	ph, e := syscall.OpenProcess(0x0100|0x0001, false, uint32(cmd.Process.Pid))
	if e != nil {
		closeJob()
		return nil, e
	}
	defer syscall.CloseHandle(ph)
	v, _, e = kernel.NewProc("AssignProcessToJobObject").Call(h, uintptr(ph))
	if v == 0 {
		closeJob()
		return nil, e
	}
	return closeJob, nil
}
func Kill(cmd *exec.Cmd) {
	if cmd.Process == nil {
		return
	}
	k := exec.Command("taskkill", "/T", "/F", "/PID", fmt.Sprint(cmd.Process.Pid))
	Configure(k)
	if k.Run() != nil {
		cmd.Process.Kill()
	}
}
func OpenURL(url string) error {
	p, _ := syscall.UTF16PtrFromString(url)
	verb, _ := syscall.UTF16PtrFromString("open")
	v, _, _ := syscall.NewLazyDLL("shell32.dll").NewProc("ShellExecuteW").Call(0, uintptr(unsafe.Pointer(verb)), uintptr(unsafe.Pointer(p)), 0, 0, 1)
	if v <= 32 {
		return fmt.Errorf("cannot open browser (code %d)", v)
	}
	return nil
}
func Startup(exe, home string, on bool) error {
	// Per-user login startup does not require an administrator or a scheduled task.
	name := "zsched-" + fmt.Sprintf("%x", sha256.Sum256([]byte(home)))[:16]
	advapi := syscall.NewLazyDLL("advapi32.dll")
	var key syscall.Handle
	rc, _, _ := advapi.NewProc("RegCreateKeyExW").Call(uintptr(syscall.HKEY_CURRENT_USER), uintptr(unsafe.Pointer(utf(`Software\Microsoft\Windows\CurrentVersion\Run`))), 0, 0, 0, 0x0002, 0, uintptr(unsafe.Pointer(&key)), 0)
	if rc != 0 {
		return syscall.Errno(rc)
	}
	defer syscall.RegCloseKey(key)
	if !on {
		rc, _, _ = advapi.NewProc("RegDeleteValueW").Call(uintptr(key), uintptr(unsafe.Pointer(utf(name))))
		if rc == 2 {
			return nil
		}
	} else {
		value := syscall.StringToUTF16(fmt.Sprintf(`"%s" daemon --home "%s" --tray`, exe, home))
		rc, _, _ = advapi.NewProc("RegSetValueExW").Call(uintptr(key), uintptr(unsafe.Pointer(utf(name))), 0, 1, uintptr(unsafe.Pointer(&value[0])), uintptr(len(value)*2))
	}
	if rc != 0 {
		return syscall.Errno(rc)
	}
	return nil
}

type blob struct {
	N uint32
	P *byte
}

func crypt(b []byte, enc bool) ([]byte, error) {
	if len(b) == 0 {
		return nil, nil
	}
	in := blob{uint32(len(b)), &b[0]}
	out := blob{}
	dll := syscall.NewLazyDLL("crypt32.dll")
	name := "CryptUnprotectData"
	if enc {
		name = "CryptProtectData"
	}
	v, _, e := dll.NewProc(name).Call(uintptr(unsafe.Pointer(&in)), 0, 0, 0, 0, 1, uintptr(unsafe.Pointer(&out)))
	if v == 0 {
		return nil, e
	}
	defer kernel.NewProc("LocalFree").Call(uintptr(unsafe.Pointer(out.P)))
	return append([]byte(nil), unsafe.Slice(out.P, int(out.N))...), nil
}
func Protect(b []byte) ([]byte, error)   { return crypt(b, true) }
func Unprotect(b []byte) ([]byte, error) { return crypt(b, false) }

var _ = os.ErrNotExist

func OpenTerminal(exe string, args []string) error {
	cmd := exec.Command(exe, args...)
	cmd.SysProcAttr = &syscall.SysProcAttr{CreationFlags: 0x00000010}
	if e := cmd.Start(); e != nil {
		return e
	}
	go cmd.Wait()
	return nil
}
