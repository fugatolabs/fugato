//go:build darwin

package fugato

import (
	"syscall"
	"unsafe"
)

// Darwin waitid supports WNOWAIT, unlike wait4. siginfo_t is smaller than this
// aligned buffer; its contents are intentionally unused. Only observe the child
// selected by PID, leaving reaping to os/exec.Cmd.Wait after group cleanup.
func waitNativeExit(pid int) error {
	var info [32]uint64
	for {
		_, _, errno := syscall.Syscall6(syscall.SYS_WAITID, 1, uintptr(pid), uintptr(unsafe.Pointer(&info[0])), syscall.WEXITED|syscall.WNOWAIT, 0, 0)
		if errno == syscall.EINTR {
			continue
		}
		if errno != 0 {
			return errno
		}
		return nil
	}
}
