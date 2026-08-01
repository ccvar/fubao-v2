package browsers

import (
	"bufio"
	"bytes"
	"os"
	"os/exec"
	"runtime"
	"strconv"
	"strings"
)

const (
	estimatedInstanceBytes = uint64(700 * 1024 * 1024)
	minimumReserveBytes    = uint64(2 * 1024 * 1024 * 1024)
	maximumAutoInstances   = 24
)

type ResourcePressure string

const (
	PressureUnknown     ResourcePressure = "unknown"
	PressureNormal      ResourcePressure = "normal"
	PressureConstrained ResourcePressure = "constrained"
	PressureCritical    ResourcePressure = "critical"
)

type ResourceSnapshot struct {
	CPUCount        int              `json:"cpu_count"`
	MemoryTotal     uint64           `json:"memory_total_bytes"`
	MemoryAvailable uint64           `json:"memory_available_bytes"`
	Pressure        ResourcePressure `json:"pressure"`
}

type Capacity struct {
	Mode                 string           `json:"mode"`
	Total                int              `json:"total"`
	Running              int              `json:"running"`
	Waiting              int              `json:"waiting"`
	RecommendedLimit     int              `json:"recommended_limit"`
	EffectiveLimit       int              `json:"effective_limit"`
	AvailableSlots       int              `json:"available_slots"`
	EstimatedPerInstance uint64           `json:"estimated_per_instance_bytes"`
	Resources            ResourceSnapshot `json:"resources"`
	Message              string           `json:"message"`
}

func detectResources() ResourceSnapshot {
	total, available := detectMemory()
	pressure := PressureUnknown
	if total > 0 && available <= total {
		ratio := float64(available) / float64(total)
		switch {
		case ratio < 0.12:
			pressure = PressureCritical
		case ratio < 0.25:
			pressure = PressureConstrained
		default:
			pressure = PressureNormal
		}
	}
	return ResourceSnapshot{
		CPUCount:        maxInt(1, runtime.NumCPU()),
		MemoryTotal:     total,
		MemoryAvailable: available,
		Pressure:        pressure,
	}
}

func recommendedLimit(resources ResourceSnapshot) int {
	cpuLimit := maxInt(1, resources.CPUCount)
	if resources.MemoryTotal == 0 {
		return clampInt(maxInt(1, cpuLimit/2), 1, maximumAutoInstances)
	}
	reserve := resources.MemoryTotal / 4
	if reserve < minimumReserveBytes {
		reserve = minimumReserveBytes
	}
	usable := uint64(0)
	if resources.MemoryTotal > reserve {
		usable = resources.MemoryTotal - reserve
	}
	memoryLimit := maxInt(1, int(usable/estimatedInstanceBytes))
	return clampInt(minInt(cpuLimit, memoryLimit), 1, maximumAutoInstances)
}

func effectiveLimit(resources ResourceSnapshot, recommended, running int) int {
	limit := recommended
	switch resources.Pressure {
	case PressureConstrained:
		limit = maxInt(1, recommended*3/4)
	case PressureCritical:
		// Existing work is never killed automatically. Critical pressure only
		// closes admission until memory recovers.
		limit = running
	}
	return maxInt(running, limit)
}

func capacityMessage(resources ResourceSnapshot, running, waiting, limit int) string {
	if resources.Pressure == PressureCritical {
		return "系统可用内存偏低，已暂停启动新实例"
	}
	if waiting > 0 {
		return "已达到当前安全并发，等待资源恢复后自动启动"
	}
	if running >= limit {
		return "已达到当前建议并发上限"
	}
	return "Go 引擎正在按本机资源自动控制运行并发"
}

func detectMemory() (uint64, uint64) {
	switch runtime.GOOS {
	case "darwin":
		return detectDarwinMemory()
	case "linux":
		return detectLinuxMemory()
	default:
		return 0, 0
	}
}

func detectDarwinMemory() (uint64, uint64) {
	totalOutput, err := exec.Command("/usr/sbin/sysctl", "-n", "hw.memsize").Output()
	if err != nil {
		return 0, 0
	}
	total, err := strconv.ParseUint(strings.TrimSpace(string(totalOutput)), 10, 64)
	if err != nil {
		return 0, 0
	}
	vmOutput, err := exec.Command("/usr/bin/vm_stat").Output()
	if err != nil {
		return total, 0
	}
	pageSize := uint64(4096)
	availablePages := uint64(0)
	scanner := bufio.NewScanner(bytes.NewReader(vmOutput))
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if strings.Contains(line, "page size of") {
			fields := strings.Fields(line)
			for index, field := range fields {
				if field == "of" && index+1 < len(fields) {
					if value, parseErr := strconv.ParseUint(fields[index+1], 10, 64); parseErr == nil {
						pageSize = value
					}
				}
			}
			continue
		}
		name, rawValue, ok := strings.Cut(line, ":")
		if !ok {
			continue
		}
		switch strings.TrimSpace(name) {
		case "Pages free", "Pages inactive", "Pages speculative", "Pages purgeable":
			value := strings.TrimSuffix(strings.TrimSpace(rawValue), ".")
			if pages, parseErr := strconv.ParseUint(value, 10, 64); parseErr == nil {
				availablePages += pages
			}
		}
	}
	available := availablePages * pageSize
	if available > total {
		available = total
	}
	return total, available
}

func detectLinuxMemory() (uint64, uint64) {
	data, err := os.ReadFile("/proc/meminfo")
	if err != nil {
		return 0, 0
	}
	values := map[string]uint64{}
	scanner := bufio.NewScanner(bytes.NewReader(data))
	for scanner.Scan() {
		fields := strings.Fields(scanner.Text())
		if len(fields) < 2 {
			continue
		}
		value, parseErr := strconv.ParseUint(fields[1], 10, 64)
		if parseErr == nil {
			values[strings.TrimSuffix(fields[0], ":")] = value * 1024
		}
	}
	return values["MemTotal"], values["MemAvailable"]
}

func minInt(left, right int) int {
	if left < right {
		return left
	}
	return right
}

func maxInt(left, right int) int {
	if left > right {
		return left
	}
	return right
}

func clampInt(value, minimum, maximum int) int {
	return minInt(maxInt(value, minimum), maximum)
}
