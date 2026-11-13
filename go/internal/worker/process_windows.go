//go:build windows

package worker

import (
	"os/exec"
	"strconv"
	"syscall"
	"unsafe"
)

const (
	createNoWindow          = 0x08000000
	createNewProcessGroup   = 0x00000200
	jobObjectExtendedLimits = 9
	jobObjectKillOnClose    = 0x00002000
	processSetQuota         = 0x0100
	processTerminate        = 0x0001
)

type jobBasicLimitInformation struct {
	PerProcessUserTimeLimit int64
	PerJobUserTimeLimit     int64
	LimitFlags              uint32
	MinimumWorkingSetSize   uintptr
	MaximumWorkingSetSize   uintptr
	ActiveProcessLimit      uint32
	Affinity                uintptr
	PriorityClass           uint32
	SchedulingClass         uint32
}

type jobIOCounters struct {
	ReadOperationCount  uint64
	WriteOperationCount uint64
	OtherOperationCount uint64
	ReadTransferCount   uint64
	WriteTransferCount  uint64
	OtherTransferCount  uint64
}

type jobExtendedLimitInformation struct {
	BasicLimitInformation jobBasicLimitInformation
	IOCounters            jobIOCounters
	ProcessMemoryLimit    uintptr
	JobMemoryLimit        uintptr
	PeakProcessMemoryUsed uintptr
	PeakJobMemoryUsed     uintptr
}

var (
	kernel32                     = syscall.NewLazyDLL("kernel32.dll")
	procCreateJobObjectW         = kernel32.NewProc("CreateJobObjectW")
	procSetInformationJobObject  = kernel32.NewProc("SetInformationJobObject")
	procAssignProcessToJobObject = kernel32.NewProc("AssignProcessToJobObject")
	procTerminateJobObject       = kernel32.NewProc("TerminateJobObject")
	procOpenProcess              = kernel32.NewProc("OpenProcess")
	procCloseHandle              = kernel32.NewProc("CloseHandle")
)

// setShellCmdLine gives cmd.exe a raw command line instead of letting Go escape
// the shell string as a single argument. Go's exec uses MSVCRT-style escaping
// (\") which cmd.exe does not understand, so embedded double quotes were
// mangled. `cmd.exe /S /C "<cmd>"` makes cmd strip the outer quotes and run the
// command verbatim, preserving embedded quotes and special characters.
func setShellCmdLine(cmd *exec.Cmd, shellCmd string) {
	cmd.SysProcAttr = &syscall.SysProcAttr{CmdLine: `cmd.exe /S /C "` + shellCmd + `"`}
}

func startProcess(cmd *exec.Cmd) (*proc, error) {
	// CREATE_NO_WINDOW keeps CLI children out of the worker console;
	// CREATE_NEW_PROCESS_GROUP gives Ctrl+C-equivalent fallback semantics.
	// Preserve any CmdLine set by setShellCmdLine rather than clobbering it.
	if cmd.SysProcAttr == nil {
		cmd.SysProcAttr = &syscall.SysProcAttr{}
	}
	cmd.SysProcAttr.CreationFlags |= createNoWindow | createNewProcessGroup
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	p := &proc{pid: cmd.Process.Pid}
	if h, err := createJob(); err == nil {
		if err := assignJob(h, p.pid); err == nil {
			p.job = h
		} else {
			closeHandle(h)
		}
	}
	return p, nil
}

func createJob() (uintptr, error) {
	h, _, err := procCreateJobObjectW.Call(0, 0)
	if h == 0 {
		if err == nil {
			err = syscall.EINVAL
		}
		return 0, err
	}
	var info jobExtendedLimitInformation
	info.BasicLimitInformation.LimitFlags = jobObjectKillOnClose
	r1, _, err := procSetInformationJobObject.Call(h, jobObjectExtendedLimits, uintptr(unsafe.Pointer(&info)), unsafe.Sizeof(info))
	if r1 == 0 {
		closeHandle(h)
		if err == nil {
			err = syscall.EINVAL
		}
		return 0, err
	}
	return h, nil
}

func assignJob(job uintptr, pid int) error {
	h, _, err := procOpenProcess.Call(processSetQuota|processTerminate, 0, uintptr(uint32(pid)))
	if h == 0 {
		if err == nil {
			err = syscall.EINVAL
		}
		return err
	}
	defer closeHandle(h)
	r1, _, err := procAssignProcessToJobObject.Call(job, h)
	if r1 == 0 {
		if err == nil {
			err = syscall.EINVAL
		}
		return err
	}
	return nil
}

func attachProcess(pid int) uintptr {
	h, err := createJob()
	if err != nil {
		return 0
	}
	if err := assignJob(h, pid); err != nil {
		closeHandle(h)
		return 0
	}
	return h
}

func closeHandle(h uintptr) {
	if h != 0 {
		procCloseHandle.Call(h)
	}
}

func (p *proc) kill() {
	if p == nil {
		return
	}
	if p.job != 0 {
		procTerminateJobObject.Call(p.job, 1)
		return
	}
	exec.Command("taskkill", "/F", "/T", "/PID", strconv.Itoa(p.pid)).Run()
}

func (p *proc) close() {
	if p == nil {
		return
	}
	if p.job != 0 {
		closeHandle(p.job)
		p.job = 0
	}
}
