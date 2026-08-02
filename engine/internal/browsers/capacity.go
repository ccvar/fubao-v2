package browsers

import (
	"bufio"
	"bytes"
	"os"
	"os/exec"
	"runtime"
	"strconv"
	"strings"
	"time"
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
	CPUUsage        float64          `json:"cpu_usage_percent"`
	MemoryTotal     uint64           `json:"memory_total_bytes"`
	MemoryAvailable uint64           `json:"memory_available_bytes"`
	Pressure        ResourcePressure `json:"pressure"`
}

type Capacity struct {
	Mode                   string           `json:"mode"`
	Total                  int              `json:"total"`
	Running                int              `json:"running"`
	Waiting                int              `json:"waiting"`
	RecommendedLimit       int              `json:"recommended_limit"`
	CPURecommendedLimit    int              `json:"cpu_recommended_limit"`
	MemoryRecommendedLimit int              `json:"memory_recommended_limit"`
	MaximumAutoInstances   int              `json:"maximum_auto_instances"`
	MemoryReserveBytes     uint64           `json:"memory_reserve_bytes"`
	EffectiveLimit         int              `json:"effective_limit"`
	AvailableSlots         int              `json:"available_slots"`
	EstimatedPerInstance   uint64           `json:"estimated_per_instance_bytes"`
	Resources              ResourceSnapshot `json:"resources"`
	Message                string           `json:"message"`
}

type capacityRecommendation struct {
	limit         int
	cpuLimit      int
	memoryLimit   int
	memoryReserve uint64
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
		CPUUsage:        detectCPUUsage(),
		MemoryTotal:     total,
		MemoryAvailable: available,
		Pressure:        pressure,
	}
}

func detectCPUUsage() float64 {
	switch runtime.GOOS {
	case "darwin":
		output, err := exec.Command("/usr/bin/top", "-l", "1", "-n", "0").Output()
		if err == nil {
			return parseDarwinCPUUsage(string(output))
		}
	case "linux":
		return detectLinuxCPUUsage()
	}
	return 0
}

func parseDarwinCPUUsage(output string) float64 {
	for _, line := range strings.Split(output, "\n") {
		if !strings.Contains(line, "CPU usage:") {
			continue
		}
		fields := strings.Fields(strings.ReplaceAll(line, ",", ""))
		for index := 0; index+1 < len(fields); index++ {
			if fields[index+1] != "idle" {
				continue
			}
			idle, err := strconv.ParseFloat(strings.TrimSuffix(fields[index], "%"), 64)
			if err == nil {
				return clampPercent(100 - idle)
			}
		}
	}
	return 0
}

func detectLinuxCPUUsage() float64 {
	firstTotal, firstIdle, ok := readLinuxCPUTimes()
	if !ok {
		return 0
	}
	time.Sleep(100 * time.Millisecond)
	secondTotal, secondIdle, ok := readLinuxCPUTimes()
	if !ok || secondTotal <= firstTotal {
		return 0
	}
	totalDelta := secondTotal - firstTotal
	idleDelta := secondIdle - firstIdle
	if idleDelta > totalDelta {
		idleDelta = totalDelta
	}
	return clampPercent(100 * float64(totalDelta-idleDelta) / float64(totalDelta))
}

func readLinuxCPUTimes() (uint64, uint64, bool) {
	data, err := os.ReadFile("/proc/stat")
	if err != nil {
		return 0, 0, false
	}
	line, _, _ := strings.Cut(string(data), "\n")
	fields := strings.Fields(line)
	if len(fields) < 5 || fields[0] != "cpu" {
		return 0, 0, false
	}
	values := make([]uint64, 0, len(fields)-1)
	for _, field := range fields[1:] {
		value, parseErr := strconv.ParseUint(field, 10, 64)
		if parseErr != nil {
			return 0, 0, false
		}
		values = append(values, value)
	}
	total := uint64(0)
	for _, value := range values {
		total += value
	}
	idle := values[3]
	if len(values) > 4 {
		idle += values[4]
	}
	return total, idle, true
}

func clampPercent(value float64) float64 {
	if value < 0 {
		return 0
	}
	if value > 100 {
		return 100
	}
	return value
}

func calculateCapacityRecommendation(resources ResourceSnapshot) capacityRecommendation {
	// Browser WebViews spend much of their time waiting on network, media and
	// rendering work. A 1.5x CPU allowance uses that headroom while the memory
	// pressure gate still protects the machine from unsafe admission.
	cpuLimit := maxInt(1, resources.CPUCount*3/2)
	if resources.MemoryTotal == 0 {
		limit := clampInt(maxInt(1, cpuLimit/2), 1, maximumAutoInstances)
		return capacityRecommendation{limit: limit, cpuLimit: cpuLimit, memoryLimit: limit}
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
	return capacityRecommendation{
		limit:         clampInt(minInt(cpuLimit, memoryLimit), 1, maximumAutoInstances),
		cpuLimit:      cpuLimit,
		memoryLimit:   memoryLimit,
		memoryReserve: reserve,
	}
}

func recommendedLimit(resources ResourceSnapshot) int {
	return calculateCapacityRecommendation(resources).limit
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
