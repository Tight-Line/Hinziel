// Package local is a forge.Forge for a bare git repository on the factory's
// host, with merge requests kept as files next to it. It's for trying a
// factory before it's connected to the team's real forge, and for teams
// without one.
//
// Each merge request is a directory, <MergeRequests>/<id>/, holding
// merge-request.json (branches, title, labels, state), description.md and
// comments.md. Review a merge request with
// `git -C <Repo> diff <target>...<source>`, and merge it with git. To close
// one, set "state" in its merge-request.json to anything but "open"; a later
// run for the same branch then opens a new one.
//
// # Credentials
//
//   - None. The factory's host needs git, and read and write access to the
//     repository and the merge request directory. Create the repository
//     with `git init --bare <Repo>` and push the team's branch to it.
package local

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/Tight-Line/hinziel/core"
	"github.com/Tight-Line/hinziel/forge"
)

// Options configure the repository and where merge requests are kept.
type Options struct {
	// Repo is the bare repository, e.g. "/srv/git/app.git".
	Repo string
	// MergeRequests is the directory merge requests are kept in;
	// "" = next to Repo, named after it: "/srv/git/app.merge-requests".
	MergeRequests string
	// DefaultBranch is the branch runs start from; "" = the repository's HEAD.
	DefaultBranch string
}

// Forge is a local bare repository with merge requests on disk.
type Forge struct {
	opts Options
	mu   sync.Mutex
}

var (
	_ forge.Forge                = (*Forge)(nil)
	_ forge.ChangeRequestUpdater = (*Forge)(nil)
	_ core.Checker               = (*Forge)(nil)
)

// New returns a local forge.
func New(opts Options) *Forge {
	return &Forge{opts: opts}
}

// mergeRequest is merge-request.json.
type mergeRequest struct {
	ID           string    `json:"id"`
	State        string    `json:"state"` // "open"; anything else is closed
	SourceBranch string    `json:"source_branch"`
	TargetBranch string    `json:"target_branch"`
	Title        string    `json:"title"`
	Draft        bool      `json:"draft,omitempty"`
	Labels       []string  `json:"labels,omitempty"`
	Reviewers    []string  `json:"reviewers,omitempty"`
	Opened       time.Time `json:"opened"`
}

const metaFile = "merge-request.json"

func (f *Forge) repo() (string, error) {
	if f.opts.Repo == "" {
		return "", errors.New("Repo is required")
	}
	return filepath.Abs(f.opts.Repo)
}

func (f *Forge) dir() (string, error) {
	if f.opts.MergeRequests != "" {
		return filepath.Abs(f.opts.MergeRequests)
	}
	repo, err := f.repo()
	if err != nil {
		return "", err
	}
	return strings.TrimSuffix(repo, ".git") + ".merge-requests", nil
}

func (f *Forge) Remote(ctx context.Context) (*forge.GitRemote, error) {
	repo, err := f.repo()
	if err != nil {
		return nil, err
	}
	branch := f.opts.DefaultBranch
	if branch == "" {
		out, err := git(ctx, repo, "symbolic-ref", "--short", "HEAD")
		if err != nil {
			return nil, fmt.Errorf("default branch of %s: %w", repo, err)
		}
		branch = out
	}
	return &forge.GitRemote{URL: repo, DefaultBranch: branch}, nil
}

// OpenChangeRequest returns the open merge request for the source branch,
// or opens one. The branch must have been pushed.
func (f *Forge) OpenChangeRequest(ctx context.Context, in forge.ChangeRequestInput) (*forge.ChangeRequest, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	dir, err := f.dir()
	if err != nil {
		return nil, err
	}
	all, err := f.list(dir)
	if err != nil {
		return nil, err
	}
	for _, mr := range all {
		if mr.State == "open" && mr.SourceBranch == in.SourceBranch {
			return changeRequest(dir, mr), nil
		}
	}
	repo, err := f.repo()
	if err != nil {
		return nil, err
	}
	if _, err := git(ctx, repo, "rev-parse", "--verify", "--quiet", "refs/heads/"+in.SourceBranch); err != nil {
		return nil, fmt.Errorf("source branch %q is not in %s", in.SourceBranch, repo)
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}
	// The next free number; Mkdir claims it, even against another process.
	n := len(all) + 1
	for ; ; n++ {
		err := os.Mkdir(filepath.Join(dir, strconv.Itoa(n)), 0o755)
		if err == nil {
			break
		}
		if !errors.Is(err, os.ErrExist) {
			return nil, err
		}
	}
	mr := &mergeRequest{ID: strconv.Itoa(n), State: "open", Opened: time.Now().UTC()}
	if err := write(dir, mr, in); err != nil {
		return nil, err
	}
	return changeRequest(dir, mr), nil
}

// UpdateChangeRequest rewrites the merge request's title and description.
func (f *Forge) UpdateChangeRequest(_ context.Context, cr *forge.ChangeRequest, in forge.ChangeRequestInput) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	dir, err := f.dir()
	if err != nil {
		return err
	}
	mr, err := read(filepath.Join(dir, cr.ID))
	if err != nil {
		return err
	}
	return write(dir, mr, in)
}

// CommentChangeRequest appends the comment to comments.md.
func (f *Forge) CommentChangeRequest(_ context.Context, cr *forge.ChangeRequest, msg core.Message) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	dir, err := f.dir()
	if err != nil {
		return err
	}
	if _, err := read(filepath.Join(dir, cr.ID)); err != nil {
		return err
	}
	file, err := os.OpenFile(filepath.Join(dir, cr.ID, "comments.md"), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	_, err = fmt.Fprintf(file, "## %s\n\n%s\n\n", time.Now().UTC().Format(time.RFC3339), msg.Markdown())
	return errors.Join(err, file.Close())
}

// Check verifies that Repo is a bare repository with the default branch.
func (f *Forge) Check(ctx context.Context) error {
	if err := core.NeedTools("git"); err != nil {
		return err
	}
	if err := core.NeedSettings(map[string]string{"Repo": f.opts.Repo}); err != nil {
		return err
	}
	repo, _ := f.repo()
	if out, err := git(ctx, repo, "rev-parse", "--is-bare-repository"); err != nil || out != "true" {
		return fmt.Errorf("%s is not a bare git repository (create it with `git init --bare %s`)", repo, repo)
	}
	remote, err := f.Remote(ctx)
	if err != nil {
		return err
	}
	if _, err := git(ctx, repo, "rev-parse", "--verify", "--quiet", "refs/heads/"+remote.DefaultBranch); err != nil {
		return fmt.Errorf("%s has no branch %s yet; push it first", repo, remote.DefaultBranch)
	}
	return nil
}

// list reads every merge request in dir.
func (f *Forge) list(dir string) ([]*mergeRequest, error) {
	entries, err := os.ReadDir(dir)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var out []*mergeRequest
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		mr, err := read(filepath.Join(dir, e.Name()))
		if errors.Is(err, os.ErrNotExist) {
			continue // claimed by another process, not written yet
		}
		if err != nil {
			return nil, err
		}
		out = append(out, mr)
	}
	return out, nil
}

func read(dir string) (*mergeRequest, error) {
	data, err := os.ReadFile(filepath.Join(dir, metaFile))
	if err != nil {
		return nil, err
	}
	var mr mergeRequest
	if err := json.Unmarshal(data, &mr); err != nil {
		return nil, fmt.Errorf("%s: %w", filepath.Join(dir, metaFile), err)
	}
	return &mr, nil
}

// write saves the merge request with in's branches, title and description.
func write(dir string, mr *mergeRequest, in forge.ChangeRequestInput) error {
	mr.SourceBranch, mr.TargetBranch = in.SourceBranch, in.TargetBranch
	mr.Title, mr.Draft, mr.Labels, mr.Reviewers = in.Title, in.Draft, in.Labels, in.Reviewers
	data, err := json.MarshalIndent(mr, "", "  ")
	if err != nil {
		return err
	}
	mrDir := filepath.Join(dir, mr.ID)
	title := mr.Title
	if mr.Draft {
		title = "Draft: " + title
	}
	desc := fmt.Sprintf("# %s\n\n`%s` → `%s`\n\n%s\n", title, mr.SourceBranch, mr.TargetBranch, in.Description.Markdown())
	if err := os.WriteFile(filepath.Join(mrDir, "description.md"), []byte(desc), 0o644); err != nil {
		return err
	}
	// The metadata last: a directory with it is a complete merge request.
	return os.WriteFile(filepath.Join(mrDir, metaFile), append(data, '\n'), 0o644)
}

func changeRequest(dir string, mr *mergeRequest) *forge.ChangeRequest {
	return &forge.ChangeRequest{
		ID:           mr.ID,
		URL:          "file://" + filepath.Join(dir, mr.ID, "description.md"),
		SourceBranch: mr.SourceBranch,
		TargetBranch: mr.TargetBranch,
	}
}

func git(ctx context.Context, dir string, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, "git", append([]string{"-C", dir}, args...)...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("git %s: %w: %s", strings.Join(args, " "), err, strings.TrimSpace(string(out)))
	}
	return strings.TrimSpace(string(out)), nil
}
