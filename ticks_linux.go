package main

import (
	"syscall"
	"unsafe"
)

const clockMonotonicRaw = 4

// tickCount matches what Wine reports to the game as GetTickCount: CLOCK_MONOTONIC_RAW in ms.
func tickCount() uint32 {
	var ts syscall.Timespec
	if _, _, errno := syscall.Syscall(syscall.SYS_CLOCK_GETTIME, clockMonotonicRaw, uintptr(unsafe.Pointer(&ts)), 0); errno != 0 {
		syscall.Syscall(syscall.SYS_CLOCK_GETTIME, 1, uintptr(unsafe.Pointer(&ts)), 0)
	}
	return uint32(uint64(ts.Sec)*1000 + uint64(ts.Nsec)/1000000)
}
