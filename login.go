package hinziel

import (
	"bufio"
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"slices"

	"github.com/Tight-Line/hinziel/core"
)

// stdin is where `login` reads what the human pastes back; tests replace it.
var stdin io.Reader = os.Stdin

// loginTries is how many links `login` offers a component before giving up.
const loginTries = 3

// login logs the host in to every component that logs in through a human
// (core.Login) and isn't logged in yet, or, with --force, is: it prints a
// link, reads what the human pastes back and finishes the login, once per
// component even if it plays several parts (environment and deployer).
func login(ctx context.Context, out io.Writer, factories []Factory, args []string) error {
	fs := flag.NewFlagSet("login", flag.ContinueOnError)
	fs.SetOutput(out)
	factory := fs.String("factory", "", "log in only this factory's components")
	force := fs.Bool("force", false, "log in again even where the host is logged in")
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

	in := bufio.NewReader(stdin)
	var done []core.Login
	var errs []error
	for _, f := range factories {
		for _, c := range components(&f) {
			l, ok := c.value.(core.Login)
			if !ok || !selected(selectors, c.label) || slices.Contains(done, l) {
				continue
			}
			done = append(done, l)
			name := f.Name + ": " + c.label
			if !*force {
				in, err := l.LoggedIn(ctx)
				if err != nil {
					errs = append(errs, fmt.Errorf("%s: %w", name, err))
					continue
				}
				if in {
					fmt.Fprintf(out, "ok    %s: already logged in (--force to log in again)\n", name)
					continue
				}
			}
			if err := loginOne(ctx, out, in, name, l); err != nil {
				errs = append(errs, fmt.Errorf("%s: %w", name, err))
				continue
			}
			fmt.Fprintf(out, "ok    %s: logged in\n", name)
		}
	}
	if len(done) == 0 {
		return errors.New("nothing here logs in through a link")
	}
	return errors.Join(errs...)
}

// loginOne offers links until one login works, up to loginTries.
func loginOne(ctx context.Context, out io.Writer, in *bufio.Reader, name string, l core.Login) error {
	var err error
	for try := 1; try <= loginTries; try++ {
		var how string
		if how, err = l.StartLogin(ctx); err != nil {
			return err
		}
		fmt.Fprintf(out, "\n%s\n\n%s\n\n> ", name, how)
		answer, readErr := in.ReadString('\n')
		if answer == "" && readErr != nil {
			return fmt.Errorf("nothing pasted: %w", readErr)
		}
		if err = l.FinishLogin(ctx, answer); err == nil {
			return nil
		}
		fmt.Fprintf(out, "That didn't work: %v\n", err)
	}
	return err
}
