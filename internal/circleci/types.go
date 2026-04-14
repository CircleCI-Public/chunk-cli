package circleci

type Sandbox struct {
	ID             string `json:"id"`
	Name           string `json:"name"`
	OrganizationID string `json:"organization_id"`
	Image          string `json:"image,omitempty"`
}

type listSandboxesResponse struct {
	Items []Sandbox `json:"items"`
}

type ExecRequest struct {
	Command string   `json:"command"`
	Args    []string `json:"args,omitempty"`
}

type ExecResponse struct {
	CommandID string `json:"command_id"`
	PID       int    `json:"pid"`
	Stdout    string `json:"stdout"`
	Stderr    string `json:"stderr"`
	ExitCode  int    `json:"exit_code"`
}

type AddSSHKeyRequest struct {
	PublicKey string `json:"public_key"`
}

type AddSSHKeyResponse struct {
	URL string `json:"url"`
}

type TriggerRunRequest struct {
	AgentType          string                 `json:"agent_type"`
	DefinitionID       string                 `json:"definition_id"`
	CheckoutBranch     string                 `json:"checkout_branch"`
	TriggerSource      string                 `json:"trigger_source"`
	ChunkEnvironmentID *string                `json:"chunk_environment_id"`
	Parameters         map[string]interface{} `json:"parameters"`
	Stats              *TriggerRunStats       `json:"stats,omitempty"`
}

type TriggerRunStats struct {
	Prompt         string `json:"prompt"`
	CheckoutBranch string `json:"checkout_branch"`
}

type RunResponse struct {
	RunID      string `json:"runId,omitempty"`
	PipelineID string `json:"pipelineId,omitempty"`
}

type CreateSandboxRequest struct {
	OrganizationID string `json:"organization_id"`
	Name           string `json:"name"`
	Provider       string `json:"provider,omitempty"`
	Image          string `json:"image,omitempty"`
}

type Snapshot struct {
	ID             string `json:"id"`
	OrganizationID string `json:"organization_id"`
	Name           string `json:"name"`
	Tag            string `json:"tag,omitempty"`
}

type CreateSnapshotRequest struct {
	SandboxID string `json:"sandbox_id"`
	Name      string `json:"name"`
}
