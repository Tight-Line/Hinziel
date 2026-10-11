package hinziel

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"slices"
	"strings"
	"syscall"

	"github.com/Tight-Line/hinziel/core"
	"github.com/Tight-Line/hinziel/internal/engine"
	"github.com/Tight-Line/hinziel/internal/envcheck"
	"github.com/Tight-Line/hinziel/internal/store"
)

// Main is the entry point of a factory binary. It validates the factories,
// runs the command given on the command line and exits.
func Main(factories ...Factory) {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	err := Run(ctx, os.Args[1:], os.Stdout, factories...)
	stop()
	if err != nil {
		fmt.Fprintf(os.Stderr, "%s: %v\n", filepath.Base(os.Args[0]), err)
		os.Exit(1)
	}
}

// Run is Main without the process handling, for tests and embedding.
// Output, including run progress, goes to out.
func Run(ctx context.Context, args []string, out io.Writer, factories ...Factory) error {
	if err := validateAll(factories); err != nil {
		return err
	}
	envcheck.Checked()
	if len(args) > 0 && args[0] == "check" {
		return check(ctx, out, factories, args[1:])
	}
	if err := missingEnvError(); err != nil {
		return err
	}
	if len(args) == 0 {
		usage(out)
		return nil
	}
	cmd, args := args[0], args[1:]
	switch cmd {
	case "help", "-h", "--help":
		usage(out)
		return nil
	case "deploy", "retrieve":
		return agentCommand(ctx, out, cmd)
	case "daemon", "env":
		return fmt.Errorf("%s: not implemented yet (milestone M2)", cmd)
	}

	c := &cli{out: out, factories: factories}
	if err := c.open(); err != nil {
		return err
	}
	switch cmd {
	case "run":
		return c.run(ctx, args)
	case "status":
		return c.status(ctx, args)
	case "logs":
		return c.logs(ctx, args)
	case "resume":
		return c.withRun(ctx, args, c.eng.Resume)
	case "approve":
		return c.withRun(ctx, args, c.eng.Approve)
	case "cancel":
		return c.withRun(ctx, args, c.eng.Cancel)
	}
	return fmt.Errorf("unknown command %q (try 'help')", cmd)
}

func validateAll(factories []Factory) error {
	if len(factories) == 0 {
		return errors.New("no factories given")
	}
	var errs []error
	seen := map[string]bool{}
	for i := range factories {
		f := &factories[i]
		if seen[f.Name] {
			errs = append(errs, fmt.Errorf("duplicate factory name %q", f.Name))
		}
		seen[f.Name] = true
		if err := f.validate(); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

func missingEnvError() error {
	if missing := envcheck.Missing(); len(missing) > 0 {
		return fmt.Errorf("missing environment variables: %s", strings.Join(missing, ", "))
	}
	return nil
}

// stateDir is where runs are kept: $HINZIEL_STATE_DIR, else
// $XDG_STATE_HOME/hinziel, else ~/.local/state/hinziel.
func stateDir() (string, error) {
	if d := os.Getenv("HINZIEL_STATE_DIR"); d != "" {
		return d, nil
	}
	if d := os.Getenv("XDG_STATE_HOME"); d != "" {
		return filepath.Join(d, "hinziel"), nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".local", "state", "hinziel"), nil
}

func newEngine(out io.Writer, factories []Factory) (*engine.Engine, error) {
	dir, err := stateDir()
	if err != nil {
		return nil, err
	}
	st, err := store.New(dir)
	if err != nil {
		return nil, err
	}
	var projects []*engine.Project
	for i := range factories {
		projects = append(projects, factories[i].project())
	}
	return engine.New(st, projects, engine.Options{Out: out, Config: envcheck.LateError}), nil
}

func usage(out io.Writer) {
	bin := filepath.Base(os.Args[0])
	fmt.Fprintf(out, `Usage: %[1]s <command> [arguments]

Commands:
  run [--factory F] <ticket-id> [--gate STEP]...
                                 start a run for a ticket and follow it
  status [run-id]                list runs or show one
  logs [-f] <run-id>             print a run's log (-f: follow it)
  resume <run-id>                continue a failed, needs_input, waiting or interrupted run
  approve <run-id>               pass the gate a run is waiting at and continue it
  cancel <run-id>                cancel a run that isn't executing
  check [--factory F] [component]...
                                 test credentials and the tools this host needs;
                                 components as listed by check, e.g. tracker, repo, "deployer api"
  daemon                         run triggers and execute runs continuously (not yet)
  env up | list | destroy        manage environments (not yet)

Used by the agent inside a run (found via $HINZIEL_SOCKET):
  deploy                         deploy every repo's pushed HEAD to the environment
  retrieve                       pull environment changes into the working tree
`, bin)
}

// check runs core.Checker on every component that implements it, or on the
// components args select. Missing environment variables are one of its
// failures rather than a reason not to start, so an env file can be filled
// in any order and checked as it goes. With a selection they're only a note:
// a selected component whose settings are empty reports that itself.
func check(ctx context.Context, out io.Writer, factories []Factory, args []string) error {
	fs := flag.NewFlagSet("check", flag.ContinueOnError)
	fs.SetOutput(out)
	factory := fs.String("factory", "", "check only this factory")
	selectors, err := parse(fs, args)
	if err != nil {
		return err
	}
	if *factory != "" {
		i := slices.IndexFunc(factories, func(f Factory) bool { return f.Name == *factory })
		if i < 0 {
			return fmt.Errorf("unknown factory %q", *factory)
		}
		factories = factories[i : i+1]
	}
	if err := checkSelectors(factories, selectors); err != nil {
		return err
	}

	failed := 0
	if err := missingEnvError(); err != nil {
		if len(selectors) == 0 {
			failed++
			fmt.Fprintf(out, "FAIL  hinziel: %v\n", err)
		} else {
			fmt.Fprintf(out, "note  hinziel: %v\n", err)
		}
	}
	// The engine clones, branches and pushes with git.
	if selected(selectors, "hinziel") {
		if err := core.NeedTools("git"); err != nil {
			failed++
			fmt.Fprintf(out, "FAIL  hinziel: %v\n", err)
		} else {
			fmt.Fprintln(out, "ok    hinziel: git")
		}
	}
	for _, f := range factories {
		for _, c := range components(&f) {
			ch, ok := c.value.(core.Checker)
			if !ok || !selected(selectors, c.label) {
				continue
			}
			if err := ch.Check(ctx); err != nil {
				failed++
				fmt.Fprintf(out, "FAIL  %s: %s: %v\n", f.Name, c.label, err)
				continue
			}
			fmt.Fprintf(out, "ok    %s: %s\n", f.Name, c.label)
		}
	}
	if failed > 0 {
		return fmt.Errorf("%d check(s) failed", failed)
	}
	return nil
}

// selected reports whether a check labelled label runs: every one without
// selectors, else those a selector names in full ("deployer api") or by
// kind ("deployer").
func selected(selectors []string, label string) bool {
	if len(selectors) == 0 {
		return true
	}
	kind, _, _ := strings.Cut(label, " ")
	return slices.Contains(selectors, label) || slices.Contains(selectors, kind)
}

// checkSelectors rejects a selector that matches nothing, listing what can
// be checked, so a typo doesn't pass as a check with nothing to fail.
func checkSelectors(factories []Factory, selectors []string) error {
	labels := []string{"hinziel"}
	for _, f := range factories {
		for _, c := range components(&f) {
			if _, ok := c.value.(core.Checker); ok && !slices.Contains(labels, c.label) {
				labels = append(labels, c.label)
			}
		}
	}
	for _, sel := range selectors {
		if !slices.ContainsFunc(labels, func(l string) bool { return selected([]string{sel}, l) }) {
			return fmt.Errorf("check: nothing to check called %q; choose from: %s", sel, strings.Join(labels, ", "))
		}
	}
	return nil
}

type component struct {
	label string
	value any
}

func components(f *Factory) []component {
	cs := []component{{"tracker", f.Tracker}, {"agent", f.Agent}}
	for _, r := range f.Repos {
		cs = append(cs, component{"repo " + r.Name, r.Forge})
	}
	if f.Environment != nil {
		cs = append(cs, component{"environment", f.Environment})
	}
	if f.DNS != nil {
		cs = append(cs, component{"dns", f.DNS})
	}
	for _, d := range f.Deployers {
		cs = append(cs, component{"deployer " + d.Name, d.Deployer})
	}
	for _, t := range f.Triggers {
		cs = append(cs, component{"trigger " + t.Name, t.Trigger})
	}
	for i, n := range f.Notifiers {
		cs = append(cs, component{fmt.Sprintf("notifier %d", i), n.Notifier})
	}
	return cs
}
