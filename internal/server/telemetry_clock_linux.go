//go:build linux

package server

import (
	"os"
	"strings"
	"syscall"
	"unsafe"
)

// hostClock identifies this machine's monotonic clock and reads it.
//
// A latency console that hears from several processes on one host can only
// subtract their timestamps if they are on the same clock, and wall time is not
// good enough for hops of a few milliseconds. CLOCK_MONOTONIC is what Node's
// process.hrtime and Python's time.monotonic_ns read; the kernel's boot id says
// which boot — and so which clock — a reading belongs to.
func hostClock() (id string, now func() (int64, bool)) {
	id = "golive:unknown"
	if raw, err := os.ReadFile("/proc/sys/kernel/random/boot_id"); err == nil {
		if boot := strings.TrimSpace(string(raw)); boot != "" {
			id = "boot:" + boot
		}
	}
	return id, func() (int64, bool) {
		var ts syscall.Timespec
		const clockMonotonic = 1
		if _, _, errno := syscall.Syscall(syscall.SYS_CLOCK_GETTIME, clockMonotonic, uintptr(unsafe.Pointer(&ts)), 0); errno != 0 {
			return 0, false
		}
		return ts.Sec*1e9 + ts.Nsec, true
	}
}
