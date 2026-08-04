package browsers

import (
	"math"
	"testing"
)

func TestParseDarwinCPUUsage(t *testing.T) {
	output := "Processes: 510 total\nCPU usage: 12.50% user, 7.25% sys, 80.25% idle\n"
	usage := parseDarwinCPUUsage(output)
	if math.Abs(usage-19.75) > 0.001 {
		t.Fatalf("CPU usage = %.2f, want 19.75", usage)
	}
}

func TestParseDarwinCPUUsageRejectsUnknownOutput(t *testing.T) {
	if usage := parseDarwinCPUUsage("CPU information unavailable"); usage != 0 {
		t.Fatalf("CPU usage = %.2f, want 0", usage)
	}
}

func TestCPUUsageFromSamples(t *testing.T) {
	usage := cpuUsageFromSamples(1_000, 600, 1_400, 760)
	if math.Abs(usage-60) > 0.001 {
		t.Fatalf("CPU usage = %.2f, want 60", usage)
	}
}

func TestCPUUsageFromSamplesRejectsInvalidRange(t *testing.T) {
	if usage := cpuUsageFromSamples(1_000, 600, 900, 500); usage != 0 {
		t.Fatalf("CPU usage = %.2f, want 0", usage)
	}
}
