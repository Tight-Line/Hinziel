package hinziel

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"time"

	"github.com/Tight-Line/hinziel/run"
	"github.com/Tight-Line/hinziel/tracker"
	"github.com/Tight-Line/hinziel/trigger"
)

// daemon starts every factory's triggers and executes the runs they submit,
// one at a time, until ctx is done. Triggers that receive HTTP get their
// handlers under /triggers/<factory>/<trigger>/ on --listen; pollers need
// no listener.
func (c *cli) daemon(ctx context.Context, args []string) error {
	fs := c.flags("daemon")
	listen := fs.String("listen", "", "address to serve HTTP triggers on, e.g. 127.0.0.1:8080; empty = none")
	pos, err := parse(fs, args)
	if err != nil {
		return err
	}
	if len(pos) > 0 {
		return errors.New("usage: daemon [--listen ADDR]")
	}

	queue := make(chan run.ID, 64)
	mux := http.NewServeMux()
	started := 0
	for _, f := range c.factories {
		for _, t := range f.Triggers {
			sub := http.NewServeMux()
			d := &dispatcher{cli: c, factory: f.Name, queue: queue}
			if err := t.Trigger.Start(ctx, d, sub); err != nil {
				return fmt.Errorf("trigger %s/%s: %w", f.Name, t.Name, err)
			}
			prefix := "/triggers/" + f.Name + "/" + t.Name
			mux.Handle(prefix+"/", http.StripPrefix(prefix, sub))
			fmt.Fprintf(c.out, "trigger %s/%s started\n", f.Name, t.Name)
			started++
		}
	}
	if started == 0 {
		return errors.New("no factory has triggers, so the daemon has nothing to do; start runs with `run`")
	}
	if *listen != "" {
		ln, err := net.Listen("tcp", *listen)
		if err != nil {
			return err
		}
		srv := &http.Server{Handler: mux, ReadHeaderTimeout: 10 * time.Second}
		go srv.Serve(ln)
		defer srv.Close()
		fmt.Fprintf(c.out, "serving HTTP triggers on %s\n", ln.Addr())
	}
	fmt.Fprintln(c.out, "waiting for runs (Ctrl-C stops; a run in progress is interrupted and can be resumed)")

	// One run at a time: runs share the host, and an agent can be heavy.
	for {
		select {
		case <-ctx.Done():
			return nil
		case id := <-queue:
			if err := c.eng.Execute(ctx, id); err != nil {
				fmt.Fprintf(c.out, "run %s: %v\n", id, err)
			}
			c.report(ctx, id)
			// A run waiting on something (CI, a deploy) is checked again in
			// the background, so it doesn't hold up the queue.
			if r, err := c.eng.Store().Load(ctx, id); err == nil && r.Status == run.StatusWaiting {
				go func() {
					if err := c.follow(ctx, id); err != nil {
						fmt.Fprintf(c.out, "run %s: %v\n", id, err)
					}
				}()
			}
		}
	}
}

// dispatcher is the engine as one factory's triggers see it.
type dispatcher struct {
	cli     *cli
	factory string
	queue   chan<- run.ID
}

var _ trigger.Dispatcher = (*dispatcher)(nil)

// Submit creates the run and queues it for execution.
func (d *dispatcher) Submit(ctx context.Context, req run.Request) (run.ID, error) {
	req.Factory = d.factory
	r, err := d.cli.eng.Submit(ctx, req)
	if err != nil {
		return "", err
	}
	select {
	case d.queue <- r.ID:
	default:
		return r.ID, fmt.Errorf("run %s is queued behind too many others; start it with `resume %s`", r.ID, r.ID)
	}
	fmt.Fprintf(d.cli.out, "run %s queued for ticket %s (%s)\n", r.ID, req.Ticket.ID, req.RequestedBy)
	return r.ID, nil
}

func (d *dispatcher) Get(ctx context.Context, id run.ID) (*run.Snapshot, error) {
	r, err := d.cli.eng.Store().Load(ctx, id)
	if err != nil {
		return nil, err
	}
	if r.Factory != d.factory {
		return nil, fmt.Errorf("run %s belongs to another factory", id)
	}
	return &r.Snapshot, nil
}

func (d *dispatcher) Latest(ctx context.Context, ticket tracker.TicketRef) (*run.Snapshot, error) {
	runs, err := d.cli.eng.Store().List(ctx, run.Filter{Factory: d.factory, Ticket: &ticket, Limit: 1})
	if err != nil || len(runs) == 0 {
		return nil, err
	}
	return &runs[0], nil
}
