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

type AddSshKeyRequest struct {
	PublicKey string `json:"public_key"`
}

type AddSshKeyResponse struct {
	URL string `json:"url"`
}

type TriggerRunRequest struct {
	AgentType          string                 `json:"agent_type"`
	DefinitionID       string                 `json:"definition_id"`
	CheckoutBranch     string                 `json:"checkout_branch"`
	TriggerSource      string                 `json:"trigger_source"`
	ChunkEnvironmentID *string                `json:"chunk_environment_id"`
	Parameters         map[string]interface{} `json:"parameters"`
}

type RunResponse struct {
	RunID      string `json:"runId,omitempty"`
	PipelineID string `json:"pipelineId,omitempty"`
}

type CreateSandboxRequest struct {
	OrganizationID string `json:"organization_id"`
	Name           string `json:"name"`
	Image          string `json:"image,omitempty"`
}
