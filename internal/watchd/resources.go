package watchd

import "time"

// Resource sampling, as a client needs to judge a sample's age.
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
)
