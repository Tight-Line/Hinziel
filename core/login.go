package core

import "context"

// Login is implemented by a component whose service a human can log the
// factory's host in to, through a link: the `login` command, run once when a
// host is set up. A component may also ask for a login mid-run, on the
// ticket (NeedsInputError), when it finds the host logged out.
type Login interface {
	// LoggedIn reports whether the host is logged in and the login works.
	LoggedIn(ctx context.Context) (bool, error)
	// StartLogin begins a login and returns what the human must do, as
	// plain text: the link, and what to paste back.
	StartLogin(ctx context.Context) (string, error)
	// FinishLogin completes the login begun last, from what the human
	// pasted back.
	FinishLogin(ctx context.Context, answer string) error
}
