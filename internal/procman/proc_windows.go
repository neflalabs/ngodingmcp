//go:build windows

package procman

import (
	"os/exec"
	"strconv"
	"syscall"
	"unsafe"

	"golang.org/x/sys/windows"
)

// prepareCmd is a no-op on Windows; tree ownership is established after the
// process starts by assigning it to a Job Object.
func prepareCmd(cmd *exec.Cmd) {}

type processTree struct {
	pid int
	job windows.Handle
}

// attachTree assigns the new process to a Job Object configured with
// JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE, so children are cleaned up with it.
// If the job cannot be created (for example because of restrictive parent
// jobs), it falls back to taskkill-based tree termination.
func attachTree(pid int) (*processTree, error) {
	tree := &processTree{pid: pid}

	job, err := windows.CreateJobObject(nil, nil)
	if err != nil {
		return tree, nil
	}
	info := windows.JOBOBJECT_EXTENDED_LIMIT_INFORMATION{}
	info.BasicLimitInformation.LimitFlags = windows.JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE
	if _, err := windows.SetInformationJobObject(
		job,
		windows.JobObjectExtendedLimitInformation,
		uintptr(unsafe.Pointer(&info)),
		uint32(unsafe.Sizeof(info)),
	); err != nil {
		_ = windows.CloseHandle(job)
		return tree, nil
	}

	ph, err := windows.OpenProcess(windows.PROCESS_SET_QUOTA|windows.PROCESS_TERMINATE, false, uint32(pid))
	if err != nil {
		_ = windows.CloseHandle(job)
		return tree, nil
	}
	defer windows.CloseHandle(ph)
	if err := windows.AssignProcessToJobObject(job, ph); err != nil {
		_ = windows.CloseHandle(job)
		return tree, nil
	}

	tree.job = job
	return tree, nil
}

// Terminate asks the process tree to close (taskkill without /F).
func (t *processTree) Terminate() error {
	return t.taskkill(false)
}

// Kill forcibly terminates the process tree.
func (t *processTree) Kill() error {
	if t.job != 0 {
		return windows.TerminateJobObject(t.job, 1)
	}
	return t.taskkill(true)
}

func (t *processTree) taskkill(force bool) error {
	if t.pid <= 0 {
		return nil
	}
	args := []string{"/T", "/PID", strconv.Itoa(t.pid)}
	if force {
		args = append([]string{"/F"}, args...)
	}
	cmd := exec.Command("taskkill", args...)
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true}
	return cmd.Run()
}

func (t *processTree) Close() {
	if t.job != 0 {
		_ = windows.CloseHandle(t.job)
		t.job = 0
	}
}
