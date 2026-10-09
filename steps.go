package heinzel

// Step names a pipeline step, for Factory.Gates and Factory.DisabledSteps.
// The names also appear in run state, `status` output and notifications.
type Step string

// The pipeline, in order.
const (
	StepFetchTicket          Step = "fetch_ticket"
	StepPrepareWorkspace     Step = "prepare_workspace"
	StepProvisionEnvironment Step = "provision_environment"
	StepDeployBase           Step = "deploy_base"
	StepImplement            Step = "implement"
	StepRetrieve             Step = "retrieve"
	StepFinalizeBranch       Step = "finalize_branch"
	StepOpenChangeRequest    Step = "open_change_request"
	StepDeploy               Step = "deploy"
	StepPublish              Step = "publish"
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
