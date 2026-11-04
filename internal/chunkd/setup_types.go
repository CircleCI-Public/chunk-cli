package chunkd

// SetupRequest starts a project's Setup run: it writes the project's
// .chunk/config.json and records its sidecar image, as chunk factory
// bootstrap does.
type SetupRequest struct {
	ProjectRoot string `json:"project_root"`
	OrgID       string `json:"org_id"`
}

// SetupState is where a project's Setup run stands.
type SetupState string

// Setup states.
const (
	SetupRunning   SetupState = "running"
	SetupOK        SetupState = "ok"
	SetupStuck     SetupState = "stuck"     // provisioning or snapshot failed: Retry or Continue
	SetupContinued SetupState = "continued" // the developer chose Continue without an image
	SetupFailed    SetupState = "failed"    // an error before provisioning (config, org)
)

// SetupActivity is a project's Setup run: its state, the step it is on, and
// its feed from First on.
type SetupActivity struct {
	State   SetupState `json:"state"`
	Step    string     `json:"step"`              // bootstrap.Step
	Image   string     `json:"image,omitempty"`   // the recorded snapshot once ok
	Summary string     `json:"summary,omitempty"` // the error when stuck or failed
	// Checks is how many validation commands or review prompts the
	// project's config has once the config step is done, so a queued change
	// knows a plan can start; -1 until then.
	Checks int        `json:"checks"`
	First  int        `json:"first"`
	Lines  []FeedLine `json:"lines,omitempty"`
	Total  int        `json:"total"`
}
