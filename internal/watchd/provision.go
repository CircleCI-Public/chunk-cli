package watchd

// ProvisionRequest is sent to POST /sidecar.
type ProvisionRequest struct {
	OrgID string `json:"org_id"`
	Name  string `json:"name"`
	Image string `json:"image,omitempty"`
}

// ProvisionResponse is returned from POST /sidecar.
type ProvisionResponse struct {
	SidecarID string `json:"sidecar_id"`
}
