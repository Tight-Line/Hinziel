// Package agent defines the coding agent that implements a ticket in a
// working tree (Claude Code, OpenHands, …). Implementations live in subpackages.
package agent

import (
	"context"
	"io"
	"os/exec"
	"time"

	"github.com/Tight-Line/hinziel/env"
	"github.com/Tight-Line/hinziel/tracker"
)

// Agent implements the task in the working tree. It works in a loop: edit,
// commit, push to the run branch, `hinziel deploy`, test against the
// environment. It MUST push only to the run branch, and MUST NOT open the
// change request; the engine does that, and commits whatever is left over.
type Agent interface {
	Implement(ctx context.Context, task *Task) (*Outcome, error)
}

type Task struct {
	// Workdir contains one checkout per factory repo (see Repos), all on the
	// run branch. The agent works here and may change any of them.
	Workdir string
	Repos   []Checkout
	Ticket  *tracker.Ticket
	// Environment is provisioned and running the base branch before the agent
	// starts, so it can run, test or click in it while working. Nil only if the
	// project has no environment.
	Environment *env.Environment
	Prompt      string    // built by the engine from the ticket and the factory's Instructions
	Env         []string  // process environment: whatever access the agent needs, plus HINZIEL_BIN and HINZIEL_RUN_ID for `$HINZIEL_BIN deploy`/`retrieve`
	Log         io.Writer // full agent transcript goes here (run log)
	// Sandbox is the run's container, which the factory describes. An agent
	// component MUST start every process of the agent through it; it is nil
	// only in tests. Paths in the task are the same inside as outside.
	Sandbox Sandbox
	// TempDir is for the agent component's own files, such as the file the
	// agent reports its outcome in. The agent's processes can reach it.
	TempDir string
}

// Sandbox runs the agent's processes in the run's container.
type Sandbox interface {
	// Wrap turns cmd, prepared as if for the factory's host, into a command
	// that runs inside, with the same directory, environment and standard
	// streams. Call it before cmd.Start. cmd.Args[0] is looked up inside, so
	// the program need not exist on the host. It starts the sandbox if it
	// isn't running yet.
	Wrap(ctx context.Context, cmd *exec.Cmd) error
}

type Checkout struct {
	Name string // repo name from the factory
	Path string // absolute path of the checkout, inside Task.Workdir
}

type Outcome struct {
	Summary       string `json:"summary"`        // markdown; goes into the MR description
	CommitMessage string `json:"commit_message"` // optional; engine template is the fallback
	// ManualSteps are things a human must do that the change itself can't carry
	// (e.g. org settings the Salesforce Metadata API can't deploy, a production
	// secret to set). Listed in the change request and notifications.
	ManualSteps []string `json:"manual_steps,omitempty"`
	Usage       Usage    `json:"usage"`
}

type Usage struct {
	InputTokens  int64         `json:"input_tokens"`
	OutputTokens int64         `json:"output_tokens"`
	CostUSD      float64       `json:"cost_usd"`
	Duration     time.Duration `json:"duration"`
}
