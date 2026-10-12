package hinziel

import (
	"context"
	"errors"
	"fmt"
	"text/tabwriter"
	"time"
)

// environments runs `env list` and `env destroy`.
func (c *cli) environments(ctx context.Context, args []string) error {
	if len(args) == 0 {
		return errors.New("usage: env list [--all] | env destroy [--factory F] <run-id | env-name | ticket>")
	}
	switch args[0] {
	case "list":
		return c.envList(ctx, args[1:])
	case "destroy":
		return c.envDestroy(ctx, args[1:])
	case "up":
		return errors.New("env up: not implemented yet (milestone M2)")
	}
	return fmt.Errorf("unknown env command %q (list or destroy)", args[0])
}

func (c *cli) envList(ctx context.Context, args []string) error {
	fs := c.flags("env list")
	all := fs.Bool("all", false, "include environments that were destroyed")
	pos, err := parse(fs, args)
	if err != nil {
		return err
	}
	if len(pos) > 0 {
		return errors.New("usage: env list [--all]")
	}
	envs, err := c.eng.Environments(ctx, *all)
	if len(envs) == 0 && err == nil {
		fmt.Fprintln(c.out, "no environments")
		return nil
	}
	w := tabwriter.NewWriter(c.out, 0, 0, 2, ' ', 0)
	fmt.Fprintln(w, "ENVIRONMENT\tFACTORY\tTICKET\tLATEST RUN\tSTATE\tSINCE")
	for _, en := range envs {
		ticket, latest, state, since := "", "", "exists", ""
		if s := en.Latest; s != nil {
			if s.Ticket != nil {
				ticket = s.Ticket.Key
			}
			latest = fmt.Sprintf("%s (%s)", s.ID, s.Status)
			since = s.UpdatedAt.Local().Format(time.DateTime)
		}
		switch {
		case en.Orphan:
			state = "orphan: no run knows it"
		case !en.Destroyed.IsZero():
			state, since = "destroyed", en.Destroyed.Local().Format(time.DateTime)
		}
		fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\t%s\n", en.Environment.Name, en.Factory, ticket, latest, state, since)
	}
	if ferr := w.Flush(); err == nil {
		err = ferr
	}
	return err
}

func (c *cli) envDestroy(ctx context.Context, args []string) error {
	fs := c.flags("env destroy")
	factory := fs.String("factory", "", "look only in this factory")
	pos, err := parse(fs, args)
	if err != nil {
		return err
	}
	if len(pos) != 1 {
		return errors.New("usage: env destroy [--factory F] <run-id | env-name | ticket>")
	}
	_, err = c.eng.Destroy(ctx, *factory, pos[0], "asked for by cli:"+username())
	return err
}
