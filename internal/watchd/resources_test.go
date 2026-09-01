package watchd

import (
	"bufio"
	"context"
	"strings"
	"testing"
	"time"

	"gotest.tools/v3/assert"
	"gotest.tools/v3/assert/cmp"
)

// Captured shapes of the files the remote sampler reads.
const (
	frameCgroupV2 = `cpu  100 0 50 800 50 0 0 0 0 0
mem_current 536870912
mem_max 2147483648
/dev/vda1 10737418240 3221225472 7516192768 30% /
`
	frameCgroupV2NoLimit = `cpu  200 0 100 1600 100 0 0 0 0 0
mem_current 536870912
mem_max max
/dev/vda1 10737418240 3221225472 7516192768 30% /
`
	frameCgroupV1 = `cpu  100 0 50 800 50 0 0 0 0 0
mem_current 268435456
mem_max 1073741824
/dev/vda1 10737418240 1073741824 9663676416 10% /
`
	// The host-memory fallback. MemTotal here is deliberately huge, which is
	// exactly the trap: reporting it as the sidecar's limit makes every sidecar
	// look like it has hundreds of gigabytes free.
	frameProcMeminfo = `cpu  100 0 50 800 50 0 0 0 0 0
MemTotal:       263876624 kB
MemAvailable:   131938312 kB
/dev/vda1 10737418240 3221225472 7516192768 30% /
`
)

func TestParseSampleFrameCgroupV2(t *testing.T) {
	s := parseSampleFrame(frameCgroupV2)
	assert.Check(t, s.haveCPU)
	assert.Check(t, cmp.Equal(s.memUsed, int64(536870912)))
	assert.Check(t, cmp.Equal(s.memLimit, int64(2147483648)))
	assert.Check(t, cmp.Equal(s.diskTotal, int64(10737418240)))
	assert.Check(t, cmp.Equal(s.diskUsed, int64(3221225472)))
}

func TestParseSampleFrameCgroupV1(t *testing.T) {
	s := parseSampleFrame(frameCgroupV1)
	assert.Check(t, cmp.Equal(s.memUsed, int64(268435456)))
	assert.Check(t, cmp.Equal(s.memLimit, int64(1073741824)))
}

func TestParseSampleFrameUnlimitedCgroupMemoryHasNoLimit(t *testing.T) {
	s := parseSampleFrame(frameCgroupV2NoLimit)
	// "max" means no limit, which must read as unknown rather than as zero bytes
	// available or some sentinel treated as a real number.
	assert.Check(t, cmp.Equal(s.memLimit, int64(0)))
	assert.Check(t, cmp.Equal(s.memUsed, int64(536870912)))
}

func TestParseSampleFrameProcMeminfoFallbackDerivesUsed(t *testing.T) {
	s := parseSampleFrame(frameProcMeminfo)
	// used = total - available, both converted from kB.
	wantLimit := int64(263876624) * 1024
	wantUsed := wantLimit - int64(131938312)*1024
	assert.Check(t, cmp.Equal(s.memLimit, wantLimit))
	assert.Check(t, cmp.Equal(s.memUsed, wantUsed))
	assert.Check(t, s.memUsed > 0, "derived usage must not go negative")
}

func TestParseSampleFrameGarbageIsHarmless(t *testing.T) {
	s := parseSampleFrame("total nonsense\n\n   \nnot-a-number x\n")
	assert.Check(t, !s.haveCPU)
	assert.Check(t, cmp.Equal(s.memUsed, int64(0)))
	assert.Check(t, cmp.Equal(s.memLimit, int64(0)))
}

func TestCPUPercentRequiresElapsedTime(t *testing.T) {
	first := parseSampleFrame(frameCgroupV2)
	_, sameOK := cpuPercent(first.cpu, first.cpu)
	assert.Check(t, !sameOK, "two identical readings have no elapsed time between them")
}

func TestCPUPercentDerivesFromDelta(t *testing.T) {
	prev := cpuTimes{total: 1000, idle: 900}
	cur := cpuTimes{total: 1200, idle: 1000}
	// 200 jiffies elapsed, 100 of them idle → 50% busy.
	pct, ok := cpuPercent(prev, cur)
	assert.Assert(t, ok)
	assert.Check(t, cmp.Equal(pct, 50.0))
}

func TestCPUPercentClampsAndRejectsCounterReset(t *testing.T) {
	// A sidecar restart resets counters; a negative delta must not produce a
	// wild percentage.
	_, ok := cpuPercent(cpuTimes{total: 5000, idle: 4000}, cpuTimes{total: 100, idle: 50})
	assert.Check(t, !ok, "a counter reset must yield no reading rather than a bogus one")

	// Idle moving backwards alone is also incoherent.
	_, idleOK := cpuPercent(cpuTimes{total: 1000, idle: 500}, cpuTimes{total: 1200, idle: 400})
	assert.Check(t, !idleOK)
}

func TestSplitFramesEmitsCompleteFramesOnly(t *testing.T) {
	input := "a\nb\n" + sampleFrameSep + "\nc\n" + sampleFrameSep + "\npartial\n"
	frames := make(chan string, 4)
	splitFrames(bufio.NewScanner(strings.NewReader(input)), frames)
	close(frames)

	var got []string
	for f := range frames {
		got = append(got, f)
	}
	// The trailing partial frame is withheld: emitting it would publish a
	// reading with half its fields missing.
	assert.Assert(t, cmp.Len(got, 2))
	assert.Check(t, cmp.Equal(got[0], "a\nb\n"))
	assert.Check(t, cmp.Equal(got[1], "c\n"))
}

func TestConsumeSamplesWithholdsCPUUntilSecondFrame(t *testing.T) {
	s := &sidecarSampler{}
	frames := make(chan string, 2)
	frames <- frameCgroupV2
	close(frames)
	consumeSamples(frames, s)

	got := s.get()
	assert.Assert(t, got != nil)
	assert.Check(t, cmp.Equal(got.CPUPercent, 0.0), "one frame cannot yield a CPU percentage")
	assert.Check(t, cmp.Equal(got.MemUsedBytes, int64(536870912)))
	assert.Check(t, cmp.Equal(got.MemLimitBytes, int64(2147483648)))
}

func TestConsumeSamplesDerivesCPUAcrossFrames(t *testing.T) {
	s := &sidecarSampler{}
	frames := make(chan string, 2)
	frames <- frameCgroupV2        // cpu total 1000, idle 850
	frames <- frameCgroupV2NoLimit // cpu total 2000, idle 1700
	close(frames)
	consumeSamples(frames, s)

	got := s.get()
	assert.Assert(t, got != nil)
	assert.Check(t, got.CPUPercent > 0, "a second frame must produce a CPU reading, got %v", got.CPUPercent)
	assert.Check(t, got.CPUPercent <= 100)
}

func TestResourceSamplerDoesNothingWhenNoDashboardAttached(t *testing.T) {
	r := newResourceSampler(&credentials{})
	// Never touched, so no dashboard has ever asked for a snapshot.
	assert.Check(t, !r.attached())

	r.reconcile([]SidecarState{{ID: "sc-1"}})

	r.mu.Lock()
	n := len(r.samplers)
	r.mu.Unlock()
	assert.Check(t, cmp.Equal(n, 0), "sampling must not start without an attached dashboard")
}

func TestResourceSamplerStopsSamplersWhenSidecarLeaves(t *testing.T) {
	r := newResourceSampler(&credentials{})
	r.sample = blockingSampler(make(chan string, 4))
	r.touch()
	t.Cleanup(r.stopAll)

	r.reconcile([]SidecarState{{ID: "sc-1"}, {ID: "sc-2"}})
	r.mu.Lock()
	started := len(r.samplers)
	r.mu.Unlock()
	assert.Assert(t, cmp.Equal(started, 2))

	// sc-2 disappears from the poll; its sampler must be cancelled and dropped.
	r.reconcile([]SidecarState{{ID: "sc-1"}})
	r.mu.Lock()
	_, keptOne := r.samplers["sc-1"]
	_, keptTwo := r.samplers["sc-2"]
	r.mu.Unlock()
	assert.Check(t, keptOne, "sc-1 should still be sampled")
	assert.Check(t, !keptTwo, "a sidecar that left the poll must stop being sampled")
}

func TestResourceSamplerIgnoresLocalRunner(t *testing.T) {
	r := newResourceSampler(&credentials{})
	r.touch()
	// The local runner has an empty ID and no sidecar to connect to.
	r.reconcile([]SidecarState{{ID: ""}})
	r.mu.Lock()
	n := len(r.samplers)
	r.mu.Unlock()
	assert.Check(t, cmp.Equal(n, 0))
}

func TestResourceSamplerAnnotateAttachesLatest(t *testing.T) {
	r := newResourceSampler(&credentials{})
	r.sample = blockingSampler(make(chan string, 4))
	r.touch()
	t.Cleanup(r.stopAll)
	r.reconcile([]SidecarState{{ID: "sc-1"}})

	r.mu.Lock()
	s := r.samplers["sc-1"]
	r.mu.Unlock()
	assert.Assert(t, s != nil)
	s.set(Resources{CPUPercent: 42})

	sidecars := []SidecarState{{ID: "sc-1"}, {ID: "sc-other"}}
	r.annotate(sidecars)
	assert.Assert(t, sidecars[0].Resources != nil)
	assert.Check(t, cmp.Equal(sidecars[0].Resources.CPUPercent, 42.0))
	assert.Check(t, cmp.Nil(sidecars[1].Resources))
}

func TestRemoteSamplerScriptPrefersCgroupOverProcMeminfo(t *testing.T) {
	script := remoteSamplerScript()
	// The ordering matters more than the exact text: reading /proc/meminfo first
	// would report host memory on every containerised sidecar.
	v2 := strings.Index(script, "/sys/fs/cgroup/memory.current")
	v1 := strings.Index(script, "/sys/fs/cgroup/memory/memory.usage_in_bytes")
	host := strings.Index(script, "/proc/meminfo")
	assert.Assert(t, v2 >= 0 && v1 >= 0 && host >= 0)
	assert.Check(t, v2 < v1, "cgroup v2 must be tried before v1")
	assert.Check(t, v1 < host, "cgroup must be tried before /proc/meminfo")
	assert.Check(t, cmp.Contains(script, sampleFrameSep))
}

// blockingSampler is a sample function that records which sidecars it was asked
// to sample and then waits for cancellation, standing in for a session that runs
// until the daemon stops it. It keeps lifecycle tests off the network entirely.
func blockingSampler(started chan<- string) func(context.Context, SidecarState, *sidecarSampler) error {
	return func(ctx context.Context, sc SidecarState, _ *sidecarSampler) error {
		select {
		case started <- sc.ID:
		default:
		}
		<-ctx.Done()
		return ctx.Err()
	}
}

func TestSidecarSamplerGiveUpKeepsTheLastReading(t *testing.T) {
	s := &sidecarSampler{}
	s.set(Resources{CPUPercent: 12})
	s.giveUp(time.Minute)

	// The reading has to survive: the dashboard dims a stale sample, and dropping
	// it instead would make a sidecar the daemon stopped watching look like one
	// with nothing to report.
	got := s.get()
	assert.Assert(t, got != nil)
	assert.Check(t, cmp.Equal(got.CPUPercent, 12.0))
}

func TestSidecarSamplerRetryIsClaimedOnce(t *testing.T) {
	s := &sidecarSampler{}
	assert.Check(t, !s.claimRetry(), "a sampler that never gave up is not due a retry")

	s.giveUp(time.Hour)
	assert.Check(t, !s.claimRetry(), "the cooloff has not elapsed")

	s.giveUp(0)
	assert.Check(t, s.claimRetry(), "an elapsed cooloff is due a retry")
	assert.Check(t, !s.claimRetry(), "the retry must be claimable only once")
}

func TestResourceSamplerRetriesAfterCooloff(t *testing.T) {
	started := make(chan string, 4)
	r := newResourceSampler(&credentials{})
	r.sample = blockingSampler(started)
	r.touch()
	t.Cleanup(r.stopAll)

	sidecars := []SidecarState{{ID: "sc-1"}}
	r.reconcile(sidecars)
	assert.Check(t, cmp.Equal(<-started, "sc-1"))

	r.mu.Lock()
	s := r.samplers["sc-1"]
	r.mu.Unlock()
	assert.Assert(t, s != nil)
	s.set(Resources{CPUPercent: 7})

	// Sampling gave up with the cooloff still running: no restart yet.
	s.giveUp(time.Hour)
	r.reconcile(sidecars)
	select {
	case id := <-started:
		t.Fatalf("restarted %s before the cooloff elapsed", id)
	default:
	}

	// Cooloff elapsed: the same slot is restarted, so the last reading is still
	// there for the dashboard to dim rather than being replaced by nothing.
	s.giveUp(0)
	r.reconcile(sidecars)
	assert.Check(t, cmp.Equal(<-started, "sc-1"))

	r.mu.Lock()
	same := r.samplers["sc-1"] == s
	r.mu.Unlock()
	assert.Check(t, same, "a retry must reuse the sampler slot, not replace it")
	assert.Assert(t, s.get() != nil)
	assert.Check(t, cmp.Equal(s.get().CPUPercent, 7.0))
}

func TestResourceSamplerStopCancelsTheRunningSession(t *testing.T) {
	started := make(chan string, 1)
	r := newResourceSampler(&credentials{})
	stopped := make(chan struct{})
	r.sample = func(ctx context.Context, sc SidecarState, _ *sidecarSampler) error {
		started <- sc.ID
		<-ctx.Done()
		close(stopped)
		return ctx.Err()
	}
	r.touch()

	r.reconcile([]SidecarState{{ID: "sc-1"}})
	assert.Check(t, cmp.Equal(<-started, "sc-1"))

	r.stopAll()
	select {
	case <-stopped:
	case <-time.After(2 * time.Second):
		t.Fatal("stopAll must cancel the running sampling session")
	}
}
