package engine

import (
	"context"
	"fmt"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/Tight-Line/heinzel/agent"
	"github.com/Tight-Line/heinzel/core"
	"github.com/Tight-Line/heinzel/deploy"
	"github.com/Tight-Line/heinzel/dns"
	"github.com/Tight-Line/heinzel/env"
	"github.com/Tight-Line/heinzel/forge"
	"github.com/Tight-Line/heinzel/internal/names"
	"github.com/Tight-Line/heinzel/internal/workspace"
	"github.com/Tight-Line/heinzel/tracker"
)

func (x *execution) runStep(ctx context.Context, name string) error {
	switch name {
	case StepFetchTicket:
		return x.fetchTicket(ctx)
	case StepPrepareWorkspace:
		return x.prepareWorkspace(ctx)
	case StepProvisionEnvironment:
		return x.provisionEnvironment(ctx)
	case StepDeployBase:
		// Deployers already done are skipped, so going on after a wait
		// doesn't deploy them again.
		_, err := x.deployAll(ctx, true)
		return err
	case StepImplement:
		return x.implement(ctx)
	case StepRetrieve:
		return x.retrieve(ctx)
	case StepFinalizeBranch:
		return x.finalizeBranch(ctx)
	case StepOpenChangeRequest:
		return x.openChangeRequests(ctx)
	case StepDeploy:
		_, err := x.deployAll(ctx, true)
		return err
	case StepPublish:
		return x.publish(ctx)
	}
	return fmt.Errorf("unknown step %q", name)
}

func (x *execution) fetchTicket(ctx context.Context) error {
	t, err := x.p.Tracker.Fetch(ctx, x.r.Request.Ticket.ID)
	if err != nil {
		return err
	}
	x.r.Ticket = t
	x.downloadAttachments(ctx)
	x.moveTicket(ctx, tracker.StageInProgress)
	return nil
}

// maxAttachment is the largest attachment downloaded for the agent.
const maxAttachment = 50 << 20

// downloadAttachments saves the files stored in the tracker to the run's
// directory, where the agent can read them: it has no tracker credentials.
// A file that can't be fetched keeps its URL in the prompt; it never fails
// the run.
func (x *execution) downloadAttachments(ctx context.Context) {
	f, ok := x.p.Tracker.(tracker.AttachmentFetcher)
	if !ok {
		return
	}
	dir := filepath.Join(x.e.store.Dir(x.r.ID), "attachments")
	for i := range x.r.Ticket.Attachments {
		a := &x.r.Ticket.Attachments[i]
		if !a.IsFile {
			continue
		}
		path := filepath.Join(dir, fmt.Sprintf("%02d-%s", i+1, safeFileName(a.Name)))
		if err := download(ctx, f, x.ticketID(), *a, path); err != nil {
			x.logf(StepFetchTicket, "attachment %q not downloaded: %v", a.Name, err)
			continue
		}
		a.Path = path
	}
}

func download(ctx context.Context, f tracker.AttachmentFetcher, ticket string, a tracker.Attachment, path string) error {
	rc, err := f.FetchAttachment(ctx, ticket, a)
	if err != nil {
		return err
	}
	defer rc.Close()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	out, err := os.Create(path)
	if err != nil {
		return err
	}
	n, err := io.Copy(out, io.LimitReader(rc, maxAttachment+1))
	if cerr := out.Close(); err == nil {
		err = cerr
	}
	if err == nil && n > maxAttachment {
		err = fmt.Errorf("larger than %d MB", maxAttachment>>20)
	}
	if err != nil {
		os.Remove(path)
	}
	return err
}

var unsafeFileChars = regexp.MustCompile(`[^A-Za-z0-9._-]+`)

func safeFileName(name string) string {
	name = strings.Trim(unsafeFileChars.ReplaceAllString(filepath.Base(name), "_"), "._")
	if name == "" {
		return "attachment"
	}
	return name
}

func (x *execution) reposDir() string { return filepath.Join(x.e.store.Dir(x.r.ID), "repos") }

// repo returns the run's checkout of a repo, cloning it on first use.
func (x *execution) repo(ctx context.Context, name string) (*workspace.Repo, error) {
	if ws, ok := x.repos[name]; ok {
		return ws, nil
	}
	cfg, err := x.repoConfig(name)
	if err != nil {
		return nil, err
	}
	remote, err := cfg.Forge.Remote(ctx)
	if err != nil {
		return nil, fmt.Errorf("repo %s: %w", name, err)
	}
	ws, err := workspace.Clone(ctx, remote, filepath.Join(x.reposDir(), name))
	if err != nil {
		return nil, fmt.Errorf("repo %s: %w", name, err)
	}
	x.repos[name] = ws
	return ws, nil
}

func (x *execution) repoConfig(name string) (Repo, error) {
	for _, r := range x.p.Repos {
		if r.Name == name {
			return r, nil
		}
	}
	return Repo{}, fmt.Errorf("unknown repo %q", name)
}

func (x *execution) prepareWorkspace(ctx context.Context) error {
	if x.r.Branch == "" {
		if f := x.p.Settings.BranchName; f != nil {
			x.r.Branch = f(x.r.Ticket)
		} else {
			x.r.Branch = names.Branch(x.r.Ticket.Key, x.r.Ticket.Title)
		}
	}
	for _, cfg := range x.p.Repos {
		ws, err := x.repo(ctx, cfg.Name)
		if err != nil {
			return err
		}
		// A branch left by an earlier run for the same ticket is continued.
		start := "origin/" + cfg.TargetBranch
		exists, err := ws.RemoteBranchExists(ctx, x.r.Branch)
		if err != nil {
			return err
		}
		if exists {
			start = "origin/" + x.r.Branch
		}
		// The branch stays local until someone pushes a change to it, so a
		// repo the ticket doesn't touch gets no remote branch, MR or CI run.
		if err := ws.CheckoutBranch(ctx, x.r.Branch, start); err != nil {
			return fmt.Errorf("repo %s: %w", cfg.Name, err)
		}
		x.logf(StepPrepareWorkspace, "%s: branch %s from %s", cfg.Name, x.r.Branch, start)
	}
	return nil
}

func (x *execution) provisionEnvironment(ctx context.Context) error {
	name := names.Env(x.p.Name, x.r.Ticket.Key)
	if f := x.p.Settings.EnvName; f != nil {
		name = f(x.r.Ticket)
	}
	spec := env.Spec{Name: name, Labels: map[string]string{
		"heinzel-factory": x.p.Name,
		"heinzel-ticket":  x.r.Ticket.Key,
		"heinzel-run":     string(x.r.ID),
	}}
	e, err := x.p.Provisioner.Ensure(ctx, spec)
	if err != nil {
		return err
	}
	if e.Endpoints == nil {
		e.Endpoints = map[string]string{}
	}
	for _, ep := range x.p.Endpoints {
		u := ep.URL(e)
		e.Endpoints[ep.Name] = u
		if ep.DNS == nil || x.p.DNS == nil {
			continue
		}
		typ, value := ep.DNS(e)
		if typ == "" {
			continue
		}
		parsed, err := url.Parse(u)
		if err != nil || parsed.Hostname() == "" {
			return fmt.Errorf("endpoint %s: URL %q has no host", ep.Name, u)
		}
		rec := dns.Record{Name: parsed.Hostname(), Type: typ, Value: value}
		if err := x.p.DNS.Ensure(ctx, rec); err != nil {
			return fmt.Errorf("endpoint %s: DNS: %w", ep.Name, err)
		}
		x.logf(StepProvisionEnvironment, "DNS %s %s -> %s", rec.Type, rec.Name, rec.Value)
	}
	if err := x.collectActions(ctx, e); err != nil {
		return err
	}
	x.r.Environment = e
	if err := x.save(ctx); err != nil {
		return err
	}
	x.logf(StepProvisionEnvironment, "environment %s ready", e.Name)
	return x.announceActions(ctx)
}

// deployAll runs every deployer on its repo's current HEAD, which must be
// pushed. With skipDeployed, deployers that already deployed that commit are
// skipped. It returns one line per deployer, for the agent's deploy command.
func (x *execution) deployAll(ctx context.Context, skipDeployed bool) ([]string, error) {
	var report []string
	for _, d := range x.p.Deployers {
		ws, err := x.repo(ctx, d.Repo)
		if err != nil {
			return report, err
		}
		head, err := ws.Head(ctx)
		if err != nil {
			return report, err
		}
		if prev := x.r.Deployments[d.Name]; skipDeployed && prev != nil && prev.Commit == head {
			report = append(report, fmt.Sprintf("%s: %s already deployed", d.Name, short(head)))
			x.logf("", "deploy %s: %s already deployed", d.Name, short(head))
			continue
		}
		pushed, err := ws.IsPushed(ctx)
		if err != nil {
			return report, err
		}
		if !pushed {
			return report, fmt.Errorf("deployer %s: commit %s of repo %s is not pushed", d.Name, short(head), d.Repo)
		}
		wt := filepath.Join(x.e.store.Dir(x.r.ID), "deploy", d.Name)
		if err := ws.Worktree(ctx, wt, head); err != nil {
			return report, err
		}
		branch, err := x.remoteBranch(ctx, ws, d.Repo)
		if err != nil {
			return report, err
		}
		src := &deploy.Source{Repo: d.Repo, Workdir: wt, Branch: branch, Commit: head, Ticket: x.r.Ticket}
		dep, err := d.Deployer.Deploy(ctx, x.r.Environment, src)
		if err != nil {
			return report, fmt.Errorf("deployer %s: %w", d.Name, err)
		}
		if dep.Commit == "" {
			dep.Commit = head
		}
		if x.r.Deployments == nil {
			x.r.Deployments = map[string]*deploy.Deployment{}
		}
		x.r.Deployments[d.Name] = dep
		if err := x.save(ctx); err != nil {
			return report, err
		}
		line := fmt.Sprintf("%s: deployed %s", d.Name, short(head))
		if dep.URL != "" {
			line += " -> " + dep.URL
		}
		report = append(report, line)
		x.logf("", "deploy %s", line)
	}
	return report, nil
}

// remoteBranch is the branch a repo's HEAD is deployed from: the run branch
// once it has been pushed, else the target branch it started from.
func (x *execution) remoteBranch(ctx context.Context, ws *workspace.Repo, repo string) (string, error) {
	pushed, err := ws.RemoteBranchExists(ctx, x.r.Branch)
	if err != nil || pushed {
		return x.r.Branch, err
	}
	cfg, err := x.repoConfig(repo)
	return cfg.TargetBranch, err
}

func (x *execution) implement(ctx context.Context) error {
	srv, err := x.serveSocket(ctx)
	if err != nil {
		return err
	}
	defer srv.close()

	task := &agent.Task{
		Workdir:     x.reposDir(),
		Ticket:      x.r.Ticket,
		Environment: x.r.Environment,
		Log:         x.log,
		Env: append(os.Environ(),
			"HEINZEL_BIN="+x.e.opts.Bin,
			"HEINZEL_RUN_ID="+string(x.r.ID),
			"HEINZEL_SOCKET="+srv.path,
		),
	}
	for _, cfg := range x.p.Repos {
		task.Repos = append(task.Repos, agent.Checkout{Name: cfg.Name, Path: filepath.Join(x.reposDir(), cfg.Name)})
	}
	task.Prompt = prompt(x.p, x.r, task)

	out, err := x.p.Agent.Implement(ctx, task)
	srv.close() // wait for in-flight deploys before touching run state
	if err != nil {
		return err
	}
	if out == nil {
		out = &agent.Outcome{}
	}
	x.r.Outcome = out
	return nil
}

func (x *execution) retrievers() []Deployer {
	var out []Deployer
	for _, d := range x.p.Deployers {
		if _, ok := d.Deployer.(deploy.Retriever); ok {
			out = append(out, d)
		}
	}
	return out
}

func (x *execution) retrieve(ctx context.Context) error {
	for _, d := range x.retrievers() {
		ws, err := x.repo(ctx, d.Repo)
		if err != nil {
			return err
		}
		if err := d.Deployer.(deploy.Retriever).Retrieve(ctx, x.r.Environment, ws.Dir); err != nil {
			return fmt.Errorf("deployer %s: retrieve: %w", d.Name, err)
		}
	}
	return nil
}

func (x *execution) finalizeBranch(ctx context.Context) error {
	x.r.Commits = map[string]string{}
	for _, cfg := range x.p.Repos {
		ws, err := x.repo(ctx, cfg.Name)
		if err != nil {
			return err
		}
		if _, err := ws.CommitAll(ctx, commitMessage(x.r)); err != nil {
			return fmt.Errorf("repo %s: %w", cfg.Name, err)
		}
		if err := ws.Fetch(ctx); err != nil {
			return err
		}
		differs, err := ws.DiffersFrom(ctx, "origin/"+cfg.TargetBranch)
		if err != nil {
			return fmt.Errorf("repo %s: %w", cfg.Name, err)
		}
		if !differs {
			if err := ws.DeleteRemoteBranch(ctx, x.r.Branch); err != nil {
				return fmt.Errorf("repo %s: %w", cfg.Name, err)
			}
			x.logf(StepFinalizeBranch, "%s: no changes, branch removed", cfg.Name)
			continue
		}
		if err := ws.Push(ctx, x.r.Branch); err != nil {
			return fmt.Errorf("repo %s: %w", cfg.Name, err)
		}
		head, err := ws.Head(ctx)
		if err != nil {
			return err
		}
		x.r.Commits[cfg.Name] = head
		x.logf(StepFinalizeBranch, "%s: %s pushed", cfg.Name, short(head))
	}
	if len(x.r.Commits) == 0 {
		return &core.NeedsInputError{Question: noChangesQuestion(x.r)}
	}
	return nil
}

func (x *execution) openChangeRequests(ctx context.Context) error {
	if x.r.ChangeRequests == nil {
		x.r.ChangeRequests = map[string]*forge.ChangeRequest{}
	}
	inputs := map[string]forge.ChangeRequestInput{}
	for _, cfg := range x.p.Repos {
		if _, changed := x.r.Commits[cfg.Name]; !changed {
			continue
		}
		in := forge.ChangeRequestInput{
			SourceBranch: x.r.Branch,
			TargetBranch: cfg.TargetBranch,
			Title:        x.r.Ticket.Key + ": " + x.r.Ticket.Title,
			Description:  changeRequestMessage(x.r),
			Draft:        x.p.Settings.DraftChangeRequests,
		}
		cr, err := cfg.Forge.OpenChangeRequest(ctx, in)
		if err != nil {
			return fmt.Errorf("repo %s: %w", cfg.Name, err)
		}
		x.r.ChangeRequests[cfg.Name] = cr
		inputs[cfg.Name] = in
		if err := x.save(ctx); err != nil {
			return err
		}
		x.logf(StepOpenChangeRequest, "%s: %s", cfg.Name, cr.URL)
	}
	if len(x.r.ChangeRequests) < 2 {
		return nil
	}
	// Several repos: link every change request to the others.
	for _, cfg := range x.p.Repos {
		cr, ok := x.r.ChangeRequests[cfg.Name]
		if !ok {
			continue
		}
		related := relatedMessage(x.r, cfg.Name)
		if u, ok := cfg.Forge.(forge.ChangeRequestUpdater); ok {
			in := inputs[cfg.Name]
			in.Description.Body += "\n\n" + related.Body
			in.Description.Links = append(in.Description.Links, related.Links...)
			if err := u.UpdateChangeRequest(ctx, cr, in); err != nil {
				return fmt.Errorf("repo %s: %w", cfg.Name, err)
			}
			continue
		}
		if err := cfg.Forge.CommentChangeRequest(ctx, cr, related); err != nil {
			return fmt.Errorf("repo %s: %w", cfg.Name, err)
		}
	}
	return nil
}

func (x *execution) publish(ctx context.Context) error {
	msg := publishMessage(x.r)
	for _, cfg := range x.p.Repos {
		cr, ok := x.r.ChangeRequests[cfg.Name]
		if !ok {
			continue
		}
		if err := cfg.Forge.CommentChangeRequest(ctx, cr, msg); err != nil {
			return fmt.Errorf("repo %s: %w", cfg.Name, err)
		}
	}
	if err := x.p.Tracker.Comment(ctx, x.ticketID(), msg); err != nil {
		return err
	}
	if l, ok := x.p.Tracker.(tracker.Linker); ok {
		for _, link := range msg.Links {
			if err := l.AddLink(ctx, x.ticketID(), link); err != nil {
				x.logf(StepPublish, "adding link to the ticket failed: %v", err)
			}
		}
	}
	x.moveTicket(ctx, tracker.StageInReview)
	return nil
}

func short(commit string) string {
	if len(commit) > 10 {
		return commit[:10]
	}
	return commit
}

func firstLine(s string) string {
	s, _, _ = strings.Cut(strings.TrimSpace(s), "\n")
	return s
}
