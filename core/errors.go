package core

import (
	"context"
	"time"
)

// NeedsInputError: plugin (usually the agent) cannot proceed without a human.
// Engine posts Question on the ticket, sets status needs_input, emits run.needs_input.
//
// With an ID, the answer goes back to the component: when the step runs
// again, Answer(ctx, ID) returns it. Without one (the agent's questions), the
// answer is on the ticket, which the agent sees when the run resumes.
type NeedsInputError struct {
	Question string // CommonMark
	ID       string // names the answer; "" = read it from the ticket
}

func (e *NeedsInputError) Error() string { return "needs input: " + e.Question }

// Retryable marks transient failures (rate limits, droplet not up yet).
// The engine retries the step with backoff, up to a configured limit.
type RetryableError struct{ Err error }

func (e *RetryableError) Error() string { return e.Err.Error() }
func (e *RetryableError) Unwrap() error { return e.Err }

// WaitError: the step is waiting on someone or something else, such as a CI
// pipeline, a store build, an environment booking or an approval. It is not
// a failure. The engine parks the run in the waiting status without holding
// a process, and whoever drives the run (the CLI in the foreground, the
// daemon) runs the step again after After. Steps are idempotent, so running
// one again is how it checks whether the wait is over.
type WaitError struct {
	Reason string        // what it waits for, e.g. "CI to build orders@3f2a91c"
	After  time.Duration // when to check again; 0 = one minute
}

func (e *WaitError) Error() string { return "waiting for " + e.Reason }

type answersKey struct{}

// WithAnswers returns ctx carrying answers to NeedsInputErrors, by ID. The
// engine sets it for every step.
func WithAnswers(ctx context.Context, answers map[string]string) context.Context {
	return context.WithValue(ctx, answersKey{}, answers)
}

// Answer returns the answer to the NeedsInputError with id, if someone gave
// one since it was asked.
func Answer(ctx context.Context, id string) (string, bool) {
	answers, _ := ctx.Value(answersKey{}).(map[string]string)
	a, ok := answers[id]
	return a, ok
}
