//go:build windows

package browsers

import (
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

var (
	kernel32DLL              = windows.NewLazySystemDLL("kernel32.dll")
	getSystemTimesProc       = kernel32DLL.NewProc("GetSystemTimes")
	globalMemoryStatusExProc = kernel32DLL.NewProc("GlobalMemoryStatusEx")
)

type windowsMemoryStatusEx struct {
	Length               uint32
	MemoryLoad           uint32
	TotalPhysical        uint64
	AvailablePhysical    uint64
	TotalPageFile        uint64
	AvailablePageFile    uint64
	TotalVirtual         uint64
	AvailableVirtual     uint64
	AvailableExtendedVir uint64
}

func detectWindowsCPUUsage() float64 {
	firstTotal, firstIdle, ok := readWindowsCPUTimes()
	if !ok {
		return 0
	}
	time.Sleep(100 * time.Millisecond)
	secondTotal, secondIdle, ok := readWindowsCPUTimes()
	if !ok {
		return 0
	}
	return cpuUsageFromSamples(firstTotal, firstIdle, secondTotal, secondIdle)
}

func readWindowsCPUTimes() (uint64, uint64, bool) {
	var idle windows.Filetime
	var kernel windows.Filetime
	var user windows.Filetime
	result, _, _ := getSystemTimesProc.Call(
		uintptr(unsafe.Pointer(&idle)),
		uintptr(unsafe.Pointer(&kernel)),
		uintptr(unsafe.Pointer(&user)),
	)
	if result == 0 {
		return 0, 0, false
	}
	idleTicks := windowsFiletimeTicks(idle)
	kernelTicks := windowsFiletimeTicks(kernel)
	userTicks := windowsFiletimeTicks(user)
	return kernelTicks + userTicks, idleTicks, true
}

func windowsFiletimeTicks(value windows.Filetime) uint64 {
	return uint64(value.HighDateTime)<<32 | uint64(value.LowDateTime)
}

func detectWindowsMemory() (uint64, uint64) {
	status := windowsMemoryStatusEx{Length: uint32(unsafe.Sizeof(windowsMemoryStatusEx{}))}
	result, _, _ := globalMemoryStatusExProc.Call(uintptr(unsafe.Pointer(&status)))
	if result == 0 || status.TotalPhysical == 0 {
		return 0, 0
	}
	available := status.AvailablePhysical
	if available > status.TotalPhysical {
		available = status.TotalPhysical
	}
	return status.TotalPhysical, available
}
