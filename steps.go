package skrat

import "github.com/Tight-Line/skrat/internal/engine"

// Step names a pipeline step, for Factory.Gates and Factory.DisabledSteps.
// The names also appear in run state, `status` output and notifications.
type Step string

// The pipeline, in order.
const (
	StepFetchTicket          Step = engine.StepFetchTicket
	StepPrepareWorkspace     Step = engine.StepPrepareWorkspace
	StepProvisionEnvironment Step = engine.StepProvisionEnvironment
	StepDeployBase           Step = engine.StepDeployBase
	StepImplement            Step = engine.StepImplement
	StepRetrieve             Step = engine.StepRetrieve
	StepFinalizeBranch       Step = engine.StepFinalizeBranch
	StepOpenChangeRequest    Step = engine.StepOpenChangeRequest
	StepDeploy               Step = engine.StepDeploy
	StepPublish              Step = engine.StepPublish
)

// Pipeline lists every step in execution order.
var Pipeline = []Step{
	StepFetchTicket,
	StepPrepareWorkspace,
	StepProvisionEnvironment,
	StepDeployBase,
	StepImplement,
	StepRetrieve,
	StepFinalizeBranch,
	StepOpenChangeRequest,
	StepDeploy,
	StepPublish,
}

func (s Step) valid() bool {
	for _, p := range Pipeline {
		if s == p {
			return true
		}
	}
	return false
}

func stepNames(steps []Step) []string {
	if steps == nil {
		return nil
	}
	out := make([]string, len(steps))
	for i, s := range steps {
		out[i] = string(s)
	}
	return out
}
