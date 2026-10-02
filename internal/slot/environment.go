package slot

import "context"

// ProvisionRequest is the platform-agent boundary. It contains only
// service-owned slot identity and verified environment metadata.
type ProvisionRequest struct {
	SlotID                string
	PoolID                string
	Ordinal               int
	EnvironmentGeneration uint64
	Requirement           EnvironmentRequirement
}

// ProvisionResult returns an opaque, service-internal agent handle and the
// manifest summary observed by the provisioner. It never carries credentials,
// OS usernames, SIDs, paths, endpoints, or commands.
type ProvisionResult struct {
	AgentHandle string
	Summary     EnvironmentSummary
}

// EnvironmentProvisioner keeps OS resource lifecycle behind the slot boundary.
// The control plane exchanges only validated metadata projections and opaque
// handles; credentials, paths, commands, and native identities stay inside
// the platform provisioner.
type EnvironmentProvisioner interface {
	Provision(context.Context, ProvisionRequest) (ProvisionResult, error)
	Inspect(context.Context, ProvisionRequest) (EnvironmentSummary, error)
	Retire(context.Context, ProvisionRequest) error
}

// Provisioner is a short compatibility spelling for EnvironmentProvisioner.
type Provisioner = EnvironmentProvisioner
