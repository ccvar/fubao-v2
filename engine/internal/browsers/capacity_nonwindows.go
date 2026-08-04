//go:build !windows

package browsers

func detectWindowsCPUUsage() float64 {
	return 0
}

func detectWindowsMemory() (uint64, uint64) {
	return 0, 0
}
