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
