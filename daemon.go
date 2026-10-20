package hinziel

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"slices"
	"sync"
	"time"

	"github.com/Tight-Line/hinziel/run"
	"github.com/Tight-Line/hinziel/tracker"
	"github.com/Tight-Line/hinziel/trigger"
)

// sweepEvery is how often the daemon applies the factories' Teardown.
var sweepEvery = 10 * time.Minute

// daemon starts every factory's triggers and executes the runs they submit,
// up to --runs at once, until ctx is done. Triggers that receive HTTP get their
// handlers under /triggers/<factory>/<trigger>/ on --listen; pollers need
// no listener. If a factory has a Teardown, it also destroys the
// environments it says are no longer needed, at start and every sweepEvery.
func (c *cli) daemon(ctx context.Context, args []string) error {
	fs := c.flags("daemon")
	listen := fs.String("listen", "", "address to serve HTTP triggers on, e.g. 127.0.0.1:8080; empty = none")
	runs := fs.Int("runs", 1, "how many runs execute at once; each holds a sandbox and an environment")
	pos, err := parse(fs, args)
	if err != nil {
		return err
	}
	if len(pos) > 0 {
		return errors.New("usage: daemon [--listen ADDR] [--runs N]")
	}
	if *runs < 1 {
		return errors.New("--runs must be at least 1")
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
	sweeps := slices.ContainsFunc(c.factories, func(f Factory) bool { return f.Teardown.WhenDone || f.Teardown.After > 0 })
	if started == 0 && !sweeps {
		return errors.New("no factory has triggers or a Teardown, so the daemon has nothing to do; start runs with `run`")
	}
	if sweeps {
		go c.sweep(ctx)
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
	fmt.Fprintf(c.out, "waiting for runs, up to %d at once (Ctrl-C stops; runs in progress are interrupted and can be resumed)\n", *runs)

	// Each worker executes one run at a time. Runs don't get in each other's
	// way: each has its own checkouts, sandbox and environment, and the
	// engine never lets two runs for the same ticket be unfinished.
	var wg sync.WaitGroup
	for range *runs {
		wg.Add(1)
		go func() {
			defer wg.Done()
			c.work(ctx, queue)
		}()
	}
	wg.Wait()
	return nil
}

// work executes queued runs one after another until ctx is done.
func (c *cli) work(ctx context.Context, queue <-chan run.ID) {
	for {
		select {
		case <-ctx.Done():
			return
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

// sweep destroys the environments the factories' Teardown says are no
// longer needed, now and every sweepEvery, until ctx is done.
func (c *cli) sweep(ctx context.Context) {
	for {
		if _, err := c.eng.Sweep(ctx); err != nil && ctx.Err() == nil {
			fmt.Fprintf(c.out, "teardown: %v\n", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(sweepEvery):
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

func (d *dispatcher) Teardown(ctx context.Context, ticket tracker.TicketRef, requestedBy string) (string, error) {
	en, err := d.cli.eng.Destroy(ctx, d.factory, ticket.ID, "asked for by "+requestedBy)
	if err != nil {
		return "", err
	}
	return en.Environment.Name, nil
}

// Answer gives the ticket's latest run, which must need input, the answer
// and queues it to continue.
func (d *dispatcher) Answer(ctx context.Context, ticket tracker.TicketRef, text, by string) (run.ID, error) {
	latest, err := d.Latest(ctx, ticket)
	if err != nil {
		return "", err
	}
	if latest == nil || latest.Status != run.StatusNeedsInput {
		return "", trigger.ErrNoQuestion
	}
	if err := d.cli.eng.Answer(ctx, latest.ID, text, by); err != nil {
		return "", err
	}
	select {
	case d.queue <- latest.ID:
	default:
		return latest.ID, fmt.Errorf("run %s is queued behind too many others; continue it with `resume %s`", latest.ID, latest.ID)
	}
	fmt.Fprintf(d.cli.out, "run %s answered by %s, queued to continue\n", latest.ID, by)
	return latest.ID, nil
}

// Resume reopens the ticket's latest run, which must have failed, and
// queues it to continue.
func (d *dispatcher) Resume(ctx context.Context, ticket tracker.TicketRef, by string) (run.ID, error) {
	latest, err := d.Latest(ctx, ticket)
	if err != nil {
		return "", err
	}
	if latest == nil || latest.Status != run.StatusFailed {
		return "", trigger.ErrNotFailed
	}
	if err := d.cli.eng.Reopen(ctx, latest.ID); err != nil {
		return "", err
	}
	select {
	case d.queue <- latest.ID:
	default:
		return latest.ID, fmt.Errorf("run %s is queued behind too many others; continue it with `resume %s`", latest.ID, latest.ID)
	}
	fmt.Fprintf(d.cli.out, "run %s resumed by %s\n", latest.ID, by)
	return latest.ID, nil
}

func (d *dispatcher) Latest(ctx context.Context, ticket tracker.TicketRef) (*run.Snapshot, error) {
	runs, err := d.cli.eng.Store().List(ctx, run.Filter{Factory: d.factory, Ticket: &ticket, Limit: 1})
	if err != nil || len(runs) == 0 {
		return nil, err
	}
	return &runs[0], nil
}
