package hinziel

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/user"
	"slices"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/Tight-Line/hinziel/internal/engine"
	"github.com/Tight-Line/hinziel/run"
	"github.com/Tight-Line/hinziel/tracker"
)

type cli struct {
	out       io.Writer
	factories []Factory
	eng       *engine.Engine
}

func (c *cli) open() error {
	eng, err := newEngine(c.out, c.factories)
	c.eng = eng
	return err
}

// stringList is a repeatable string flag.
type stringList []string

func (l *stringList) String() string     { return strings.Join(*l, ",") }
func (l *stringList) Set(v string) error { *l = append(*l, v); return nil }

// parse parses flags that may appear before, between or after positional
// arguments, and returns the positional ones.
func parse(fs *flag.FlagSet, args []string) ([]string, error) {
	var pos []string
	for {
		if err := fs.Parse(args); err != nil {
			return nil, err
		}
		if fs.NArg() == 0 {
			return pos, nil
		}
		pos = append(pos, fs.Arg(0))
		args = fs.Args()[1:]
	}
}

func (c *cli) flags(name string) *flag.FlagSet {
	fs := flag.NewFlagSet(name, flag.ContinueOnError)
	fs.SetOutput(c.out)
	return fs
}

func (c *cli) run(ctx context.Context, args []string) error {
	fs := c.flags("run")
	factory := fs.String("factory", "", "factory to run (needed when the binary has several)")
	var gates stringList
	fs.Var(&gates, "gate", "pause before this step (repeatable); overrides the factory's gates")
	pos, err := parse(fs, args)
	if err != nil {
		return err
	}
	if len(pos) != 1 {
		return errors.New("usage: run [--factory F] <ticket-id> [--gate STEP]...")
	}
	name, err := c.pickFactory(*factory)
	if err != nil {
		return err
	}
	var gateSteps []string
	for _, g := range gates {
		if !Step(g).valid() {
			return fmt.Errorf("--gate: unknown step %q", g)
		}
		gateSteps = append(gateSteps, g)
	}

	r, err := c.eng.Submit(ctx, run.Request{
		Factory:     name,
		Ticket:      tracker.TicketRef{ID: pos[0]},
		RequestedBy: "cli:" + username(),
		Gates:       gateSteps,
	})
	if err != nil {
		return err
	}
	fmt.Fprintf(c.out, "run %s\n", r.ID)
	err = c.eng.Execute(ctx, r.ID)
	if err == nil {
		err = c.follow(ctx, r.ID)
	}
	c.report(ctx, r.ID)
	return err
}

func (c *cli) pickFactory(name string) (string, error) {
	if name == "" {
		if len(c.factories) == 1 {
			return c.factories[0].Name, nil
		}
		return "", errors.New("this binary has several factories; choose one with --factory")
	}
	for _, f := range c.factories {
		if f.Name == name {
			return name, nil
		}
	}
	return "", fmt.Errorf("unknown factory %q", name)
}

// withRun runs an action that takes a run ID, then reports the run.
func (c *cli) withRun(ctx context.Context, args []string, action func(context.Context, run.ID) error) error {
	if len(args) != 1 {
		return errors.New("expected exactly one run id")
	}
	id := run.ID(args[0])
	err := action(ctx, id)
	if err == nil {
		err = c.follow(ctx, id)
	}
	if _, loadErr := c.eng.Store().Load(ctx, id); loadErr == nil {
		c.report(ctx, id)
	}
	return err
}

// answer gives a needs_input run the answer to its question and continues
// it. The agent's questions are answered on the ticket instead.
func (c *cli) answer(ctx context.Context, args []string) error {
	if len(args) < 2 {
		return errors.New("expected a run id and the answer")
	}
	id := run.ID(args[0])
	r, err := c.eng.Store().Load(ctx, id)
	if err != nil {
		return err
	}
	if r.Status == run.StatusNeedsInput && r.Question == nil {
		return fmt.Errorf("run %s's question is the agent's: answer on the ticket, then: resume %s", id, id)
	}
	return c.withRun(ctx, args[:1], func(ctx context.Context, id run.ID) error {
		if err := c.eng.Answer(ctx, id, strings.Join(args[1:], " "), "cli:"+username()); err != nil {
			return err
		}
		return c.eng.Execute(ctx, id)
	})
}

// report prints where a run ended up and what to do next.
func (c *cli) report(ctx context.Context, id run.ID) {
	r, err := c.eng.Store().Load(ctx, id)
	if err != nil {
		return
	}
	fmt.Fprintf(c.out, "\nrun %s: %s\n", r.ID, r.Status)
	switch r.Status {
	case run.StatusSucceeded:
		for _, name := range sortedKeys(r.ChangeRequests) {
			fmt.Fprintf(c.out, "  change request (%s): %s\n", name, r.ChangeRequests[name].URL)
		}
		if r.Environment != nil {
			for _, name := range sortedKeys(r.Environment.Endpoints) {
				fmt.Fprintf(c.out, "  %s: %s\n", name, r.Environment.Endpoints[name])
			}
		}
	case run.StatusNeedsInput:
		if r.Question != nil {
			fmt.Fprintf(c.out, "  %s\n  Continue with: answer %s <answer>\n", r.Error, r.ID)
		} else {
			fmt.Fprintf(c.out, "  %s\n  Answer on the ticket, then: resume %s\n", r.Error, r.ID)
		}
	case run.StatusAwaitingApproval:
		fmt.Fprintf(c.out, "  waiting before %s.\n", r.CurrentStep)
		for _, a := range r.Actions {
			if a.Before == r.CurrentStep && !a.Done {
				fmt.Fprintf(c.out, "  action needed: %s\n%s\n", a.Title, indentLines(a.Body, "    "))
			}
		}
		fmt.Fprintf(c.out, "  Continue with: approve %s\n", r.ID)
	case run.StatusFailed:
		fmt.Fprintf(c.out, "  failed at %s. Fix the cause, then: resume %s\n", r.CurrentStep, r.ID)
	case run.StatusWaiting:
		fmt.Fprintf(c.out, "  waiting for %s at %s; next check at %s. Continue with: resume %s\n",
			r.WaitingFor, r.CurrentStep, r.WaitUntil.Local().Format(time.DateTime), r.ID)
	}
}

func (c *cli) status(ctx context.Context, args []string) error {
	if len(args) > 1 {
		return errors.New("usage: status [run-id]")
	}
	if len(args) == 1 {
		r, err := c.eng.Store().Load(ctx, run.ID(args[0]))
		if err != nil {
			return err
		}
		c.detail(r)
		return nil
	}
	runs, err := c.eng.Store().List(ctx, run.Filter{Limit: 20})
	if err != nil {
		return err
	}
	if len(runs) == 0 {
		fmt.Fprintln(c.out, "no runs yet")
		return nil
	}
	w := tabwriter.NewWriter(c.out, 0, 0, 2, ' ', 0)
	fmt.Fprintln(w, "RUN\tFACTORY\tTICKET\tSTATUS\tSTEP\tUPDATED")
	for _, s := range runs {
		ticket := ""
		if s.Ticket == nil && s.Environment != nil {
			ticket = "-" // env up
		}
		if s.Ticket != nil {
			ticket = s.Ticket.Key
		}
		fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\t%s\n", s.ID, s.Factory, ticket, s.Status, s.CurrentStep, s.UpdatedAt.Local().Format(time.DateTime))
	}
	return w.Flush()
}

func (c *cli) detail(r *run.Run) {
	w := tabwriter.NewWriter(c.out, 0, 0, 2, ' ', 0)
	fmt.Fprintf(w, "Run\t%s\n", r.ID)
	fmt.Fprintf(w, "Factory\t%s\n", r.Factory)
	switch {
	case r.Ticket != nil:
		fmt.Fprintf(w, "Ticket\t%s: %s %s\n", r.Ticket.Key, r.Ticket.Title, r.Ticket.URL)
	case r.Request.EnvKey != "":
		fmt.Fprintf(w, "Env up\t%s\n", r.Request.EnvKey)
	default:
		fmt.Fprintf(w, "Ticket\t%s\n", r.Request.Ticket.ID)
	}
	fmt.Fprintf(w, "Status\t%s\n", r.Status)
	if r.Branch != "" {
		fmt.Fprintf(w, "Branch\t%s\n", r.Branch)
	}
	if r.Error != "" {
		fmt.Fprintf(w, "Error\t%s\n", firstLine(r.Error))
	}
	if r.Status == run.StatusWaiting {
		fmt.Fprintf(w, "Waiting for\t%s (next check %s)\n", r.WaitingFor, r.WaitUntil.Local().Format(time.DateTime))
	}
	fmt.Fprintf(w, "Started\t%s\n", r.StartedAt.Local().Format(time.DateTime))
	w.Flush()

	fmt.Fprintln(c.out, "\nSteps")
	w = tabwriter.NewWriter(c.out, 0, 0, 2, ' ', 0)
	for _, s := range r.Steps {
		line := fmt.Sprintf("  %s\t%s", s.Name, s.Status)
		if !s.Started.IsZero() && !s.Finished.IsZero() {
			line += "\t" + s.Finished.Sub(s.Started).Round(time.Second).String()
		}
		fmt.Fprintln(w, line)
	}
	w.Flush()

	if len(r.ChangeRequests) > 0 {
		fmt.Fprintln(c.out, "\nChange requests")
		for _, name := range sortedKeys(r.ChangeRequests) {
			fmt.Fprintf(c.out, "  %s: %s\n", name, r.ChangeRequests[name].URL)
		}
	}
	if r.Environment != nil {
		fmt.Fprintf(c.out, "\nEnvironment %s\n", r.Environment.Name)
		for _, name := range sortedKeys(r.Environment.Endpoints) {
			fmt.Fprintf(c.out, "  %s: %s\n", name, r.Environment.Endpoints[name])
		}
	}
	if len(r.Actions) > 0 {
		fmt.Fprintln(c.out, "\nHuman actions")
		for _, a := range r.Actions {
			state := "pending"
			if a.Done {
				state = "done"
			}
			fmt.Fprintf(c.out, "  [%s] %s\n", state, a.Title)
		}
	}
	if len(r.Deployments) > 0 {
		fmt.Fprintln(c.out, "\nDeployments")
		for _, name := range sortedKeys(r.Deployments) {
			d := r.Deployments[name]
			fmt.Fprintf(c.out, "  %s: %s at %s\n", name, shortCommit(d.Commit), d.At.Local().Format(time.DateTime))
		}
	}
}

func (c *cli) logs(ctx context.Context, args []string) error {
	fs := c.flags("logs")
	follow := fs.Bool("f", false, "keep printing new lines until the run stops")
	pos, err := parse(fs, args)
	if err != nil {
		return err
	}
	if len(pos) != 1 {
		return errors.New("usage: logs [-f] <run-id>")
	}
	id := run.ID(pos[0])
	if _, err := c.eng.Store().Load(ctx, id); err != nil {
		return err
	}
	f, err := os.Open(c.eng.Store().LogPath(id))
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	defer f.Close()
	for {
		if _, err := io.Copy(c.out, f); err != nil {
			return err
		}
		if !*follow {
			return nil
		}
		r, err := c.eng.Store().Load(ctx, id)
		if err != nil {
			return err
		}
		if r.Status != run.StatusRunning && r.Status != run.StatusQueued {
			_, err := io.Copy(c.out, f)
			return err
		}
		select {
		case <-ctx.Done():
			return nil
		case <-time.After(500 * time.Millisecond):
		}
	}
}

// agentCommand is `deploy` or `retrieve`, run by the agent inside a run.
func agentCommand(ctx context.Context, out io.Writer, command string) error {
	socket := os.Getenv("HINZIEL_SOCKET")
	if socket == "" {
		return fmt.Errorf("%s only works inside a run, where Hinziel sets HINZIEL_SOCKET", command)
	}
	output, err := engine.Call(ctx, socket, command)
	if output != "" {
		fmt.Fprintln(out, output)
	}
	return err
}

func username() string {
	if u, err := user.Current(); err == nil {
		return u.Username
	}
	return os.Getenv("USER")
}

func sortedKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	return keys
}

func firstLine(s string) string {
	s, _, _ = strings.Cut(strings.TrimSpace(s), "\n")
	return s
}

func shortCommit(c string) string {
	if len(c) > 10 {
		return c[:10]
	}
	return c
}

// indentLines prefixes every line of s.
func indentLines(s, prefix string) string {
	lines := strings.Split(strings.TrimSpace(s), "\n")
	for i, l := range lines {
		lines[i] = prefix + l
	}
	return strings.Join(lines, "\n")
}

// follow keeps a waiting run going in the foreground: it sleeps until the
// step's next check, then resumes it, until the run stops waiting. Ctrl-C
// leaves the run waiting, to be resumed later.
func (c *cli) follow(ctx context.Context, id run.ID) error {
	for {
		r, err := c.eng.Store().Load(ctx, id)
		if err != nil || r.Status != run.StatusWaiting {
			return nil
		}
		fmt.Fprintf(c.out, "waiting for %s; checking again at %s (Ctrl-C leaves it waiting: resume %s later)\n",
			r.WaitingFor, r.WaitUntil.Local().Format(time.TimeOnly), id)
		select {
		case <-ctx.Done():
			return nil
		case <-time.After(time.Until(r.WaitUntil)):
		}
		if err := c.eng.Resume(ctx, id); err != nil {
			return err
		}
	}
}
