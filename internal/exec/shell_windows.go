// Windows process-group setup and kill (File 08 §8.4.7). The shell runs in a
// new process group (CREATE_NEW_PROCESS_GROUP), and the process is also
// assigned to a Job Object whose JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE flag
// means closing the job handle kills the whole tree — the reliable,
// console-independent way to satisfy "no orphaned child after exit" on
// Windows (CTRL_BREAK alone does not reach non-console children like ping.exe
// under `go test`, which has no console; the Job Object does).
//
// The Bash.Run caller holds the job handle locally: prepareJob before Start,
// assignJobToProcess after Start (pid is known), closeJob on cancel/exit.

//go:build windows

package exec

import (
	"os/exec"
	"syscall"
	"unsafe"
)

// setProcessGroup makes cmd the leader of a new Windows process group
// (File 08 §8.4.7). The job assignment is separate (prepareJob) so the job
// handle can be held by the caller.
func setProcessGroup(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{CreationFlags: syscall.CREATE_NEW_PROCESS_GROUP}
}

// prepareJob creates a kill-on-close Job Object the started process will be
// assigned to. The caller holds the returned handle and closes it on cancel
// (closing kills the whole tree).
func prepareJob() (syscall.Handle, error) {
	const JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE = 0x2000
	kernel32 := syscall.NewLazyDLL("kernel32.dll")
	createProc := kernel32.NewProc("CreateJobObjectW")
	r1, _, e := createProc.Call(0, 0)
	if r1 == 0 {
		return 0, e
	}
	handle := syscall.Handle(r1)

	// Set the kill-on-close limit.
	const JobObjectExtendedLimitInformation = 9
	type ioLimits struct {
		ReadOpCount, WriteOpCount, OtherOpCount                   uint64
		ReadTransferCount, WriteTransferCount, OtherTransferCount uint64
	}
	type extLimit struct {
		BasicLimitInformation struct {
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
		IoInfo                ioLimits
		ProcessMemoryLimit    uintptr
		JobMemoryLimit        uintptr
		PeakProcessMemoryUsed uintptr
		PeakJobMemoryUsed     uintptr
	}
	info := extLimit{}
	info.BasicLimitInformation.LimitFlags = JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE
	setProc := kernel32.NewProc("SetInformationJobObject")
	r1, _, e = setProc.Call(uintptr(handle), uintptr(JobObjectExtendedLimitInformation), uintptr(unsafe.Pointer(&info)), uintptr(unsafe.Sizeof(info)))
	if r1 == 0 {
		_ = syscall.CloseHandle(handle)
		return 0, e
	}
	return handle, nil
}

// assignJobToProcess opens the process by pid and assigns it to the job.
func assignJobToProcess(job syscall.Handle, pid int) error {
	const PROCESS_ALL_ACCESS = 0x1F0FFF
	h, err := syscall.OpenProcess(PROCESS_ALL_ACCESS, false, uint32(pid))
	if err != nil {
		return err
	}
	defer syscall.CloseHandle(h)
	kernel32 := syscall.NewLazyDLL("kernel32.dll")
	proc := kernel32.NewProc("AssignProcessToJobObject")
	r1, _, e := proc.Call(uintptr(job), uintptr(h))
	if r1 == 0 {
		return e
	}
	return nil
}

// closeJob closes the job handle; with KILL_ON_JOB_CLOSE set, this kills the
// whole process tree assigned to the job (File 08 §8.4.7).
func closeJob(job syscall.Handle) {
	_ = syscall.CloseHandle(job)
}

// killGroup is the no-job fallback path (kept for API parity with Unix): kill
// the parent directly. Bash.Run uses the job-based path (prepareJob/closeJob)
// for reliable tree-kill; this exists so a future caller without a job still
// gets a parent kill.
func killGroup(cmd *exec.Cmd) error {
	if cmd.Process != nil {
		_ = cmd.Process.Kill()
	}
	return nil
}

// isWindows reports the host OS (always true on this build).
func isWindows() bool { return true }
