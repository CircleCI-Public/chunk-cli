package watchd

// ValidateRequest is the payload sent to POST /validate and POST
// /validate/async.
type ValidateRequest struct {
	// Args is os.Args[1:] from the caller, e.g. ["validate", "test", "--remote"].
	Args []string `json:"args"`
	// CircleCIToken is forwarded to the subprocess as CIRCLE_TOKEN.
	CircleCIToken string `json:"circleci_token,omitempty"`
	// Env is the caller's os.Environ(), forwarded verbatim to the subprocess so
	// session-identity variables (e.g. CLAUDE_CODE_SESSION_ID) reach it intact.
	Env []string `json:"env,omitempty"`
	// ProjectRoot is the repo the run applies to, already resolved by the client
	// (so it reflects the caller's --project, or its cwd). It decides what gets
	// validated, and it is also what a task is filed and fingerprinted under,
	// what a change is measured against, and what a verdict is remembered by —
	// so anything the daemon decides on a project's behalf needs it.
	//
	// It cannot be left to the daemon to infer. The daemon's own cwd is wherever
	// it was launched from, which is one arbitrary repo out of all the repos it
	// serves, so a run that resolved the project itself would validate that one
	// and report the answer under whichever project asked. Empty is accepted on
	// the synchronous path only, for a client too old to send it; such a run is
	// simply run, with nothing judged or recorded.
	ProjectRoot string `json:"project_root,omitempty"`
	// AllowAsync says the caller will accept being released before the answer
	// exists. It is an offer, not an instruction: the daemon weighs the change
	// and may hold the caller anyway.
	//
	// Only a caller that has somewhere to hear the answer later should set it.
	// A hook does — the next turn collects background results — while a developer
	// watching a terminal does not, and releasing them would leave the run's
	// output going nowhere they are looking.
	AllowAsync bool `json:"allow_async,omitempty"`
	// OrgID is the CircleCI org UUID for this project. When set and the daemon
	// has credentials, the daemon provisions a fresh sidecar for the run rather
	// than expecting one to already be registered on its filesystem.
	OrgID string `json:"org_id,omitempty"`
	// HookCodex says the run is a hook invocation from Codex, and the daemon
	// passes it on to the run as --hook-codex.
	//
	// It travels as a field rather than in Args because a remote daemon's build
	// cannot be checked, and one that predates the flag would reject the whole
	// run with a non-blocking exit — a commit gate would pass having checked
	// nothing. A daemon that predates this field ignores it instead, and the run
	// goes ahead, just without Codex's quieter output.
	HookCodex bool `json:"hook_codex,omitempty"`
	// WorkDir is the caller's working directory, which the daemon passes to the
	// subprocess as --project so it can load .chunk/config.json from the right
	// location. It equals ProjectRoot for callers whose working directory is the
	// git top-level; it differs when the .chunk directory sits below the git root.
	//
	// A daemon that predates this field ignores it and falls back to ProjectRoot,
	// which is the caller's cwd and therefore also correct in the common case.
	WorkDir string `json:"work_dir,omitempty"`
	// SidecarImage is the image the caller resolved for this run from its own
	// config, per-command override included. The daemon boots the sidecar it
	// provisions from it, since the subprocess is handed that sidecar by ID and
	// never picks an image itself. A remote daemon may not have the checkout to
	// read the config from, so the client resolving it is what makes the image
	// right there.
	//
	// Empty means the caller configured none, or predates this field; the daemon
	// then reads validation.sidecarImage from the project's config itself.
	SidecarImage string `json:"sidecar_image,omitempty"`
}

// ValidateResponse is the response from POST /validate.
type ValidateResponse struct {
	ExitCode int    `json:"exit_code"`
	Stdout   string `json:"stdout"`
	Stderr   string `json:"stderr"`
	// TaskID is set when the daemon took the run into the background rather than
	// running it here. Nothing has run yet: ExitCode is zero because there is no
	// exit code, and the result is collected on a later turn.
	TaskID string `json:"task_id,omitempty"`
	// Risk is what the daemon made of the change: a score, the facts behind it,
	// and any advice. Nil when the caller never offered to be released, since
	// then no change was judged.
	Risk *RiskSummary `json:"risk,omitempty"`
	// Reason says why the run was released or held, in words a caller can print
	// as one line. Empty when the caller never offered to be released, since
	// then there was no decision to explain.
	Reason string `json:"reason,omitempty"`
	// ClaimWarning is an advisory when another session is actively validating
	// overlapping paths. Empty in the common case (no overlap). Never a gate.
	ClaimWarning string `json:"claim_warning,omitempty"`
}

// AsyncValidateResponse acknowledges an accepted async run.
type AsyncValidateResponse struct {
	TaskID string `json:"task_id"`
	// ClaimWarning is an advisory when another session is actively validating
	// overlapping paths. Empty in the common case (no overlap). Never a gate.
	ClaimWarning string `json:"claim_warning,omitempty"`
}

// CollectResponse carries the finished results for a project.
type CollectResponse struct {
	Tasks []TaskState `json:"tasks"`
}
