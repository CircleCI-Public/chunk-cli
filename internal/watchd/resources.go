package watchd

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/CircleCI-Public/chunk-cli/internal/circleci"
)

const (
	// SampleInterval is how often the remote sampler emits a reading.
	SampleInterval = 2 * time.Second

	// StaleSamples is how many intervals a sample may age before the dashboard
	// should treat it as stale. A sampler that dies must look stalled rather than
	// look like an idle sidecar, so the last value is kept and marked, not
	// discarded.
	//
	// The budget has to cover more than SampleInterval: the reading crosses an SSH
	// connection before the daemon sees it, and the dashboard then renders that
	// same snapshot until its next 5s poll while re-evaluating the age every
	// frame. Measured against real sidecars, a healthy sampler reaches ~10s of
	// apparent age just before a poll lands, so a tighter bound flags working
	// samplers as stale for part of every cycle. Twelve seconds clears that and
	// still catches a dead sampler within two polls.
	StaleSamples = 6

	// idleShutdown is how long after the last snapshot request sampling stops.
	// Snapshot requests arrive every 5s from an attached dashboard, so this
	// tolerates a couple of missed polls before tearing connections down.
	idleShutdown = 20 * time.Second

	// sampleFrameSep terminates one sample in the remote sampler's output.
	sampleFrameSep = "---CHUNK-SAMPLE---"
)

// remoteSamplerScript is the shell loop run inside the sidecar: one long-lived
// process whose stdout the daemon reads, rather than one exec per reading.
//
// Memory is read from cgroup files where available because the sidecar is
// containerised — /proc/meminfo reports the *host's* memory, which would make
// every sidecar look like it had hundreds of gigabytes free. cgroup v2 is tried
// first, then v1, then /proc/meminfo as a last resort.
func remoteSamplerScript() string {
	secs := strconv.Itoa(int(SampleInterval.Seconds()))
	if secs == "0" {
		secs = "2"
	}
	// Bounded rather than infinite: cancelling the stream stops the daemon
	// reading, but nothing guarantees the remote process dies with it. A loop that
	// ends on its own caps how long an orphan can linger on the sidecar; run
	// simply submits another while a dashboard is still attached.
	return `n=0
while [ $n -lt ` + strconv.Itoa(samplerIterations) + ` ]; do
  n=$((n+1))
  grep '^cpu ' /proc/stat
  if [ -r /sys/fs/cgroup/memory.current ]; then
    echo "mem_current $(cat /sys/fs/cgroup/memory.current)"
    echo "mem_max $(cat /sys/fs/cgroup/memory.max 2>/dev/null || echo max)"
  elif [ -r /sys/fs/cgroup/memory/memory.usage_in_bytes ]; then
    echo "mem_current $(cat /sys/fs/cgroup/memory/memory.usage_in_bytes)"
    echo "mem_max $(cat /sys/fs/cgroup/memory/memory.limit_in_bytes)"
  else
    grep -E '^(MemTotal|MemAvailable):' /proc/meminfo
  fi
  df -PB1 "${CHUNK_SAMPLE_DIR:-.}" 2>/dev/null | tail -1
  echo ` + sampleFrameSep + `
  sleep ` + secs + `
done`
}

// samplerIterations is how many readings one remote sampler process emits before
// exiting, bounding how long an orphaned loop can survive on the sidecar.
const samplerIterations = 120

// cpuTimes is the cumulative jiffy counters from /proc/stat's cpu line.
type cpuTimes struct {
	total int64
	idle  int64
}

// sample is one parsed reading, before CPU is differenced.
type sample struct {
	cpu       cpuTimes
	haveCPU   bool
	memUsed   int64
	memLimit  int64
	diskUsed  int64
	diskTotal int64
}

// parseSampleFrame parses one frame of the remote sampler's output.
//
// Note what this deliberately does not do: derive a CPU percentage. /proc/stat
// reports cumulative jiffies since boot, so a percentage only exists between two
// frames. A single frame yields no CPU number at all, which is correct — the
// alternative is reporting a number that looks plausible and means nothing.
func parseSampleFrame(frame string) sample {
	var s sample
	sc := bufio.NewScanner(strings.NewReader(frame))
	for sc.Scan() {
		fields := strings.Fields(sc.Text())
		if len(fields) < 2 {
			continue
		}
		switch {
		case fields[0] == "cpu":
			var total, idle int64
			for i, f := range fields[1:] {
				v, err := strconv.ParseInt(f, 10, 64)
				if err != nil {
					continue
				}
				total += v
				// Fields are user, nice, system, idle, iowait, ... — idle and
				// iowait are both time the CPU was not doing work.
				if i == 3 || i == 4 {
					idle += v
				}
			}
			s.cpu = cpuTimes{total: total, idle: idle}
			s.haveCPU = total > 0

		case fields[0] == "mem_current":
			s.memUsed, _ = strconv.ParseInt(fields[1], 10, 64)

		case fields[0] == "mem_max":
			// cgroup v2 writes the literal "max" for no limit; v1 writes a
			// sentinel so large it is indistinguishable from none. Either way,
			// leaving the limit at zero is how "unknown" is represented.
			if v, err := strconv.ParseInt(fields[1], 10, 64); err == nil && v < 1<<62 {
				s.memLimit = v
			}

		case fields[0] == "MemTotal:":
			if v, err := strconv.ParseInt(fields[1], 10, 64); err == nil {
				s.memLimit = v * 1024 // /proc/meminfo is in kB
			}

		case fields[0] == "MemAvailable:":
			if v, err := strconv.ParseInt(fields[1], 10, 64); err == nil {
				// Used is derived after the loop, once MemTotal is known.
				s.memUsed = -v * 1024
			}

		case strings.HasPrefix(fields[0], "/") && len(fields) >= 4:
			// df -PB1 output: Filesystem 1B-blocks Used Available ...
			s.diskTotal, _ = strconv.ParseInt(fields[1], 10, 64)
			s.diskUsed, _ = strconv.ParseInt(fields[2], 10, 64)
		}
	}
	// MemAvailable path: used = total - available, stashed as a negative above so
	// field order in the frame does not matter.
	if s.memUsed < 0 && s.memLimit > 0 {
		s.memUsed = s.memLimit + s.memUsed
	}
	if s.memUsed < 0 {
		s.memUsed = 0
	}
	return s
}

// cpuPercent derives a utilisation percentage from two consecutive readings.
// Returns 0 and false when the readings cannot yield one.
func cpuPercent(prev, cur cpuTimes) (float64, bool) {
	dTotal := cur.total - prev.total
	dIdle := cur.idle - prev.idle
	if dTotal <= 0 || dIdle < 0 {
		return 0, false
	}
	busy := float64(dTotal-dIdle) / float64(dTotal) * 100
	if busy < 0 {
		busy = 0
	}
	if busy > 100 {
		busy = 100
	}
	return busy, true
}

// sidecarSampler is one sidecar's sampling goroutine and its latest reading.
//
// The sampler outlives its goroutine. When sampling gives up the slot stays, so
// the last reading survives and the dashboard can dim it as stale — a sampler
// that died should look stalled, not look like a sidecar with nothing to report.
type sidecarSampler struct {
	mu     sync.Mutex
	cancel context.CancelFunc
	latest *Resources
	// retryAt is when a sampler that gave up may be started again; zero while it
	// is running.
	retryAt time.Time
}

// setCancel adopts the cancel function of a freshly started goroutine.
func (s *sidecarSampler) setCancel(cancel context.CancelFunc) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.cancel = cancel
}

// stop cancels this sampler's goroutine, if it has one running.
func (s *sidecarSampler) stop() {
	s.mu.Lock()
	cancel := s.cancel
	s.cancel = nil
	s.mu.Unlock()
	if cancel != nil {
		cancel()
	}
}

// giveUp records that sampling has stopped for now, and when it may resume.
func (s *sidecarSampler) giveUp(after time.Duration) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.cancel = nil
	s.retryAt = time.Now().Add(after)
}

// claimRetry reports whether a sampler that gave up is due another attempt,
// clearing the marker so two polls cannot both act on it.
func (s *sidecarSampler) claimRetry() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.retryAt.IsZero() || time.Now().Before(s.retryAt) {
		return false
	}
	s.retryAt = time.Time{}
	return true
}

func (s *sidecarSampler) set(r Resources) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.latest = &r
}

func (s *sidecarSampler) get() *Resources {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.latest == nil {
		return nil
	}
	cp := *s.latest
	return &cp
}

// errNoClient stops a sampling session that can never succeed. The daemon
// resolves its client once at startup, so a nil one will still be nil next
// time: retrying would spin without ever producing a figure.
var errNoClient = errors.New("no CircleCI client, so resources cannot be sampled")

// resourceSampler owns one sampling goroutine per sidecar, started only while a
// dashboard is attached and stopped when the sidecar goes away or the dashboard
// does.
type resourceSampler struct {
	// client is nil when the daemon started without credentials, in which case
	// sampling never starts and the dashboard simply shows no resource figures.
	client *circleci.Client
	// sample runs one sampling session and returns when it ends. It is a field so
	// tests can drive the lifecycle — reconnects, give-up, cancellation — without
	// reaching the API.
	sample func(ctx context.Context, sc SidecarState, s *sidecarSampler) error

	mu       sync.Mutex
	samplers map[string]*sidecarSampler // keyed by sidecar ID
	lastSeen time.Time                  // last snapshot request
}

func newResourceSampler(client *circleci.Client) *resourceSampler {
	r := &resourceSampler{
		client:   client,
		samplers: make(map[string]*sidecarSampler),
	}
	r.sample = r.sampleOnce
	return r
}

// touch records that a dashboard asked for a snapshot.
func (r *resourceSampler) touch() {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.lastSeen = time.Now()
}

// attached reports whether a dashboard has asked for a snapshot recently enough
// that sampling is worth its cost.
func (r *resourceSampler) attached() bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	return !r.lastSeen.IsZero() && time.Since(r.lastSeen) < idleShutdown
}

// reconcile starts samplers for sidecars that should have one and stops those
// that should not. Called from the poll path, so it must not block: starting a
// sampler only spawns a goroutine, and stopping one only cancels a context.
func (r *resourceSampler) reconcile(sidecars []SidecarState) {
	if !r.attached() {
		r.stopAll()
		return
	}

	want := make(map[string]SidecarState, len(sidecars))
	for _, sc := range sidecars {
		if sc.ID != "" {
			want[sc.ID] = sc
		}
	}

	r.mu.Lock()
	var toStart []startRequest
	for id, sc := range want {
		s, ok := r.samplers[id]
		if !ok {
			s = &sidecarSampler{}
			r.samplers[id] = s
			toStart = append(toStart, startRequest{sidecar: sc, sampler: s})
			continue
		}
		// A sampler that gave up keeps its slot, so restarting it is a retry
		// rather than a fresh start. The cooloff is what lets a transient failure
		// recover instead of costing this sidecar its numbers for the rest of the
		// session.
		if s.claimRetry() {
			toStart = append(toStart, startRequest{sidecar: sc, sampler: s})
		}
	}
	var toStop []*sidecarSampler
	for id, s := range r.samplers {
		if _, ok := want[id]; !ok {
			toStop = append(toStop, s)
			delete(r.samplers, id)
		}
	}
	// Started under the lock: a goroutine whose sampler was stopped between being
	// recorded and adopting its cancel function could never be cancelled at all.
	for _, req := range toStart {
		ctx, cancel := context.WithCancel(context.Background())
		req.sampler.setCancel(cancel)
		go r.run(ctx, req.sidecar, req.sampler)
	}
	r.mu.Unlock()

	for _, s := range toStop {
		s.stop()
	}
}

// startRequest pairs a sidecar with the sampler slot that tracks it.
type startRequest struct {
	sidecar SidecarState
	sampler *sidecarSampler
}

// annotate attaches the latest sample to each sidecar.
func (r *resourceSampler) annotate(sidecars []SidecarState) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for i := range sidecars {
		if s, ok := r.samplers[sidecars[i].ID]; ok {
			sidecars[i].Resources = s.get()
		}
	}
}

func (r *resourceSampler) stopAll() {
	r.mu.Lock()
	samplers := make([]*sidecarSampler, 0, len(r.samplers))
	for id, s := range r.samplers {
		samplers = append(samplers, s)
		delete(r.samplers, id)
	}
	r.mu.Unlock()
	for _, s := range samplers {
		s.stop()
	}
}

// maxSamplerFailures is how many consecutive failures a sidecar gets before the
// daemon stops sampling it for sampleCooloff.
//
// Without a ceiling a sidecar that can never be sampled — deleted, or an
// unreachable image — is retried forever. Each attempt costs an API call, so a
// handful of dead sidecars turns into constant background traffic that degrades
// everything else the daemon and the CLI are doing.
const maxSamplerFailures = 3

// sampleCooloff is how long a sidecar goes unsampled after the daemon gives up on
// it. Giving up for good would let one bad minute cost a sidecar its numbers for
// the rest of the session, which reads as a broken dashboard rather than as the
// transient it was; retrying any sooner would defeat the ceiling above.
const sampleCooloff = 2 * time.Minute

// run drives one sidecar's sampler, retrying with backoff until it is cancelled
// or has failed too many times in a row, in which case reconcile starts it again
// once sampleCooloff has passed.
func (r *resourceSampler) run(ctx context.Context, sc SidecarState, s *sidecarSampler) {
	backoff := time.Second
	failures := 0
	for ctx.Err() == nil {
		err := r.sample(ctx, sc, s)
		if ctx.Err() != nil {
			return
		}
		if err == nil {
			// The remote loop ended on its own; start another immediately.
			failures = 0
			backoff = time.Second
			continue
		}

		failures++
		if failures >= maxSamplerFailures {
			log.Printf("watchd: pausing sampling of %s for %s after %d failures: %v",
				sc.ID, sampleCooloff, failures, err)
			s.giveUp(sampleCooloff)
			return
		}
		log.Printf("watchd: resource sampler for %s: %v", sc.ID, err)

		select {
		case <-ctx.Done():
			return
		case <-time.After(backoff):
		}
		if backoff < 30*time.Second {
			backoff *= 2
		}
	}
}

// sampleOnce runs the sampler on the sidecar and publishes each frame it
// produces, returning when the remote loop ends, the stream breaks, or ctx is
// cancelled. run decides whether to try again.
//
// It goes through the async exec API rather than SSH deliberately. The sidecar's
// SSH exec channel fork/execs the command string instead of interpreting it, so a
// shell script — pipes, a while loop, redirections — cannot run over it at all;
// that is why every other ExecOverSSH call in this repo is a bare `binary arg`
// invocation. The exec API takes argv as structured data ("sh", "-c", script), so
// it runs scripts correctly, and its output stream is already long-lived and
// resumable. That also removes the handshake-per-sample problem outright, with no
// persistent connection for the daemon to supervise.
func (r *resourceSampler) sampleOnce(ctx context.Context, sc SidecarState, s *sidecarSampler) error {
	client := r.client
	if client == nil {
		return errNoClient
	}

	dir := "."
	if sc.Workspace != "" {
		dir = sc.Workspace
	}

	commandID, err := client.SubmitExec(ctx, sc.ID, "sh",
		[]string{"-c", remoteSamplerScript()},
		map[string]string{"CHUNK_SAMPLE_DIR": dir},
	)
	if err != nil {
		return fmt.Errorf("submit sampler: %w", err)
	}

	frames := make(chan string)
	parsed := make(chan struct{})
	frameErr := make(chan error, 1)
	pr, pw := io.Pipe()
	go func() {
		consumeSamples(frames, s)
		close(parsed)
	}()
	go func() {
		frameErr <- readFrames(pr, frames)
		close(frames)
	}()

	_, streamErr := client.StreamOutput(ctx, commandID, "", func(_ string, data []byte) {
		_, _ = pw.Write(data)
	})
	_ = pw.Close()
	<-parsed
	if streamErr != nil {
		return streamErr
	}
	// A frame reader that stopped early otherwise looks like a healthy session:
	// the stream ends clean, and run restarts it at once — submitting a fresh
	// exec on every pass. Reporting it is what puts the backoff in play.
	if err := <-frameErr; err != nil {
		return fmt.Errorf("read sampler output: %w", err)
	}
	return nil
}

// consumeSamples parses frames off a reader, differencing CPU between them, and
// publishes each complete reading. It is separated from transport so it can be
// tested against captured sampler output with no SSH involved.
func consumeSamples(frames <-chan string, s *sidecarSampler) {
	var prev cpuTimes
	havePrev := false
	for frame := range frames {
		cur := parseSampleFrame(frame)
		res := Resources{
			MemUsedBytes:   cur.memUsed,
			MemLimitBytes:  cur.memLimit,
			DiskUsedBytes:  cur.diskUsed,
			DiskTotalBytes: cur.diskTotal,
			SampledAt:      time.Now(),
		}
		if cur.haveCPU {
			if havePrev {
				if pct, ok := cpuPercent(prev, cur.cpu); ok {
					res.CPUPercent = pct
				}
			}
			prev = cur.cpu
			havePrev = true
		}
		s.set(res)
	}
}

// splitFrames reads sampler output and emits one string per complete frame,
// returning the scanner's error if it stopped for any reason but end of input.
func splitFrames(r *bufio.Scanner, out chan<- string) error {
	var b strings.Builder
	for r.Scan() {
		line := r.Text()
		if strings.TrimSpace(line) == sampleFrameSep {
			out <- b.String()
			b.Reset()
			continue
		}
		b.WriteString(line)
		b.WriteByte('\n')
	}
	return r.Err()
}

// readFrames splits sampler output into frames and guarantees the writing side
// cannot block once it stops.
//
// bufio.Scanner gives up on a line past its 64KB token limit, and after that
// nothing reads the pipe again — so StreamOutput's callback, which writes into
// it, would block on io.Pipe forever, taking the sampler goroutine with it past
// even cancellation. Closing the read half turns that hang into a write error.
func readFrames(pr *io.PipeReader, out chan<- string) error {
	err := splitFrames(bufio.NewScanner(pr), out)
	_ = pr.CloseWithError(io.ErrClosedPipe)
	return err
}
