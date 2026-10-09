package core

// NeedsInputError: plugin (usually the agent) cannot proceed without a human.
// Engine posts Question on the ticket, sets status needs_input, emits run.needs_input.
type NeedsInputError struct{ Question string }

func (e *NeedsInputError) Error() string { return "needs input: " + e.Question }

// Retryable marks transient failures (rate limits, droplet not up yet).
// The engine retries the step with backoff, up to a configured limit.
type RetryableError struct{ Err error }

func (e *RetryableError) Error() string { return e.Err.Error() }
func (e *RetryableError) Unwrap() error { return e.Err }
