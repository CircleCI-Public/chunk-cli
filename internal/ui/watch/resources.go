package watch

import (
	"fmt"
	"time"

	"github.com/CircleCI-Public/chunk-cli/internal/watchd"
)

// renderResources formats one resource sample for the sidecar pane, or "" when
// there is nothing to show.
//
// A stale sample is dimmed rather than dropped. A sampler whose connection died
// would otherwise leave the row looking like an idle sidecar, which is the wrong
// conclusion from the same evidence — the numbers are real, they are just old.
func renderResources(st watchStyles, r *watchd.Resources) string {
	if r == nil || r.SampledAt.IsZero() {
		return ""
	}
	cpu := fmt.Sprintf("cpu %3.0f%%", r.CPUPercent)
	mem := "mem —"
	if r.MemLimitBytes > 0 {
		pct := float64(r.MemUsedBytes) / float64(r.MemLimitBytes) * 100
		mem = fmt.Sprintf("mem %3.0f%%", pct)
	} else if r.MemUsedBytes > 0 {
		mem = "mem " + humanBytes(r.MemUsedBytes)
	}

	line := cpu + "  " + mem
	if r.DiskTotalBytes > 0 {
		pct := float64(r.DiskUsedBytes) / float64(r.DiskTotalBytes) * 100
		line += fmt.Sprintf("  disk %3.0f%%", pct)
	}

	if stale(r.SampledAt) {
		return st.vdim(line + " (stale)")
	}
	return st.teal(line)
}

// stale reports whether a sample is old enough that it should not be presented as
// current.
func stale(at time.Time) bool {
	return time.Since(at) > watchd.StaleSamples*watchd.SampleInterval
}

// humanBytes formats a byte count in the largest unit that keeps it under 1024.
func humanBytes(n int64) string {
	const unit = 1024
	if n < unit {
		return fmt.Sprintf("%dB", n)
	}
	units := []string{"K", "M", "G", "T"}
	value := float64(n)
	for _, u := range units {
		value /= unit
		if value < unit {
			if value < 10 {
				return fmt.Sprintf("%.1f%s", value, u)
			}
			return fmt.Sprintf("%.0f%s", value, u)
		}
	}
	return fmt.Sprintf("%.0fP", value/unit)
}
