package heinzel

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"

	"github.com/Tight-Line/heinzel/core"
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
func Run(ctx context.Context, args []string, out io.Writer, factories ...Factory) error {
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
	if missing := missingEnvNames(); len(missing) > 0 {
		errs = append(errs, fmt.Errorf("missing environment variables: %s", strings.Join(missing, ", ")))
	}
	if err := errors.Join(errs...); err != nil {
		return err
	}

	if len(args) == 0 {
		usage(out)
		return nil
	}
	switch cmd := args[0]; cmd {
	case "help", "-h", "--help":
		usage(out)
		return nil
	case "check":
		return check(ctx, out, factories)
	case "run", "serve", "status", "logs", "resume", "approve", "cancel", "env", "deploy", "retrieve":
		return fmt.Errorf("%s: not implemented yet (milestone M0)", cmd)
	default:
		return fmt.Errorf("unknown command %q (try 'help')", cmd)
	}
}

func usage(out io.Writer) {
	bin := filepath.Base(os.Args[0])
	fmt.Fprintf(out, `Usage: %[1]s <command> [arguments]

Commands:
  run [--factory F] <ticket-id> [--gate STEP]... [--detach]
  serve                          HTTP triggers + worker pool
  status [run-id]                list runs or show one
  logs <run-id> [-f]
  resume <run-id>                continue a failed or needs_input run
  approve <run-id>               pass the current gate
  cancel <run-id>
  env up [--factory F]           provision + deploy the target branches, without a ticket
  env list | env destroy <run-id | env-name>
  check                          validate factories and test component credentials

Used by the agent inside a run (found via $HEINZEL_RUN_ID):
  deploy                         deploy every repo's pushed HEAD to the environment
  retrieve                       pull environment changes into the working tree
`, bin)
}

// check runs core.Checker on every component that implements it.
func check(ctx context.Context, out io.Writer, factories []Factory) error {
	failed := 0
	for _, f := range factories {
		for _, c := range components(&f) {
			ch, ok := c.value.(core.Checker)
			if !ok {
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
