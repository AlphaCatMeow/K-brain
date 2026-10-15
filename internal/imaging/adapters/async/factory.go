package asyncadapter

import "github.com/Stack-Cairn/K-brain/internal/imaging"

// Factory is the registration handoff; no global registry is modified.
// Workflow must be non-nil for RunningHub. Factories create implementations,
// never trusted evidence: targets remain disabled until independently verified.
func Factory(id, version string, o Options, workflow *WorkflowOptions) (imaging.Adapter, error) {
	if version != Version {
		return nil, imaging.ErrUnsupported
	}
	switch id {
	case JimengID:
		return NewJimeng(o)
	case RunningHubID:
		if workflow == nil {
			return nil, failure(imaging.CodeInvalidInput, imaging.DeliveryNotSent)
		}
		cfg := *workflow
		cfg.Options = o
		return NewRunningHub(cfg)
	default:
		return nil, imaging.ErrUnsupported
	}
}

// FactoryAvailability deliberately distinguishes implementation from deployment.
// RunningHub's output identity contract and real workflow evidence are incomplete.
type FactoryAvailability struct {
	Adapter          string
	AdapterVersion   string
	Enabled          bool
	ContractComplete bool
	LiveVerified     bool
	DisabledReason   string
}

func Availability() []FactoryAvailability {
	return []FactoryAvailability{
		{Adapter: JimengID, AdapterVersion: Version, ContractComplete: true, DisabledReason: "not enabled: generate-only static contract; live verification and official CV signature vector missing"},
		{Adapter: RunningHubID, AdapterVersion: Version, DisabledReason: "not enabled: workflow evidence and output identity contract incomplete"},
	}
}

// Options are backend-only even if accidentally passed to an HTTP JSON encoder.
func (Options) MarshalJSON() ([]byte, error) {
	return nil, failure(imaging.CodeInvalidInput, imaging.DeliveryNotSent)
}
func (WorkflowOptions) MarshalJSON() ([]byte, error) {
	return nil, failure(imaging.CodeInvalidInput, imaging.DeliveryNotSent)
}
