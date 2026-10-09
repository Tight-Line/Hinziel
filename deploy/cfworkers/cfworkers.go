// Package cfworkers is a deploy.Deployer for static sites and SPAs hosted on
// Cloudflare Workers (static assets). Each environment gets a preview alias
// of one Worker: https://<alias>-<worker>.<account subdomain>.workers.dev.
//
// It takes the app as it is: it runs the team's own build command with the
// environment variables Cloudflare's CI would set, then uploads with the
// repository's own wrangler configuration. Only the Worker name is
// overridden, so previews go to a Worker of their own and never touch the
// production ones named in the configuration.
//
// The Skrat host needs Node.js; wrangler comes from the repository's
// node_modules, or npx.
//
// # Credentials
//
//   - CLOUDFLARE_ACCOUNT_ID: shown on the account's home page and on
//     Workers & Pages in the dashboard.
//   - CLOUDFLARE_WORKERS_TOKEN: at https://dash.cloudflare.com/profile/api-tokens,
//     "Create Token" → the "Edit Cloudflare Workers" template, limited to
//     the one account.
//   - The account needs a workers.dev subdomain: opening Workers & Pages for
//     the first time asks for one.
package cfworkers

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"maps"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/Tight-Line/skrat/core"
	"github.com/Tight-Line/skrat/deploy"
	"github.com/Tight-Line/skrat/env"
	"github.com/Tight-Line/skrat/internal/apiclient"
)

// Options configure a Workers deployment.
type Options struct {
	AccountID string
	Token     string
	// Worker is the Worker previews are uploaded to, overriding the name in
	// the wrangler configuration. It's created on first use.
	Worker string
	// PreviewAlias names an environment's alias; nil = the environment name.
	PreviewAlias func(e *env.Environment) string
	// Dir is the directory in the repository holding the wrangler
	// configuration; "" = the root. Build runs here too.
	Dir string
	// Build is a shell command that builds the assets, e.g.
	// "npm ci && npm run build"; "" = nothing to build.
	Build string
	// BuildEnv is added to Build's environment.
	BuildEnv func(e *env.Environment, src *deploy.Source) map[string]string
	// Wrangler is the wrangler command; nil = {"npx", "--yes", "wrangler"}.
	Wrangler []string
	// BaseURL of the API; "" = https://api.cloudflare.com/client/v4.
	BaseURL string
}

// PreviewURL is the URL of a preview alias. Use it in an Endpoint to know the
// URL before anything is deployed.
func PreviewURL(alias, worker, accountSubdomain string) string {
	return "https://" + alias + "-" + worker + "." + accountSubdomain + ".workers.dev"
}

// Deployer uploads Worker versions.
type Deployer struct {
	opts Options
	api  *apiclient.Client

	mu        sync.Mutex
	subdomain string
}

var (
	_ deploy.Deployer = (*Deployer)(nil)
	_ core.Checker    = (*Deployer)(nil)
)

// New returns a Workers deployer.
func New(opts Options) *Deployer {
	base := strings.TrimRight(opts.BaseURL, "/")
	if base == "" {
		base = "https://api.cloudflare.com/client/v4"
	}
	return &Deployer{opts: opts, api: &apiclient.Client{
		Name:    "cloudflare",
		BaseURL: base,
		Auth:    func(r *http.Request) { r.Header.Set("Authorization", "Bearer "+opts.Token) },
	}}
}

// aliasRE is what Cloudflare accepts as a preview alias.
var aliasRE = regexp.MustCompile(`^[a-z]([a-z0-9-]*[a-z0-9])?$`)

func (d *Deployer) alias(e *env.Environment) (string, error) {
	a := e.Name
	if d.opts.PreviewAlias != nil {
		a = d.opts.PreviewAlias(e)
	}
	if !aliasRE.MatchString(a) {
		return "", fmt.Errorf("preview alias %q must be lowercase letters, digits and dashes, starting with a letter", a)
	}
	// alias-worker is one DNS label.
	if n := len(a) + 1 + len(d.opts.Worker); n > 63 {
		return "", fmt.Errorf("preview alias %q is too long for worker %q: %q would be %d characters, the limit is 63", a, d.opts.Worker, a+"-"+d.opts.Worker, n)
	}
	return a, nil
}

// Deploy builds src and uploads it as a version of the Worker, under the
// environment's preview alias.
func (d *Deployer) Deploy(ctx context.Context, e *env.Environment, src *deploy.Source) (*deploy.Deployment, error) {
	alias, err := d.alias(e)
	if err != nil {
		return nil, err
	}
	dir := filepath.Join(src.Workdir, d.opts.Dir)
	if d.opts.Build != "" {
		vars := map[string]string{}
		if d.opts.BuildEnv != nil {
			vars = d.opts.BuildEnv(e, src)
		}
		if _, err := run(ctx, dir, vars, "sh", "-c", d.opts.Build); err != nil {
			return nil, fmt.Errorf("build: %w", err)
		}
	}
	if err := d.ensureWorker(ctx, dir); err != nil {
		return nil, err
	}
	msg := fmt.Sprintf("%s %s (%s)", src.Repo, short(src.Commit), src.Branch)
	out, err := d.wrangler(ctx, dir, "versions", "upload", "--name", d.opts.Worker, "--preview-alias", alias, "--message", msg)
	if err != nil {
		return nil, fmt.Errorf("uploading version: %w", err)
	}
	u := aliasURL(out, alias, d.opts.Worker)
	if u == "" {
		sub, err := d.accountSubdomain(ctx)
		if err != nil {
			return nil, err
		}
		u = PreviewURL(alias, d.opts.Worker, sub)
	}
	info := map[string]string{"worker": d.opts.Worker, "alias": alias}
	if m := versionIDRE.FindStringSubmatch(out); m != nil {
		info["version_id"] = m[1]
	}
	return &deploy.Deployment{URL: u, Commit: src.Commit, At: time.Now(), Info: info}, nil
}

var versionIDRE = regexp.MustCompile(`Version ID:\s*([0-9a-f-]+)`)

// aliasURL finds the alias's URL in wrangler's output.
func aliasURL(out, alias, worker string) string {
	re := regexp.MustCompile(`https://` + regexp.QuoteMeta(alias+"-"+worker) + `\.[A-Za-z0-9.-]+\.workers\.dev`)
	return re.FindString(out)
}

// ensureWorker creates the Worker on first use, with preview URLs on. The
// first version is deployed as the Worker's main one; previews are served by
// alias, so what the main URL serves doesn't matter.
func (d *Deployer) ensureWorker(ctx context.Context, dir string) error {
	path := "/accounts/" + d.opts.AccountID + "/workers/scripts/" + d.opts.Worker + "/subdomain"
	var resp struct {
		Result struct {
			Enabled         bool `json:"enabled"`
			PreviewsEnabled bool `json:"previews_enabled"`
		} `json:"result"`
	}
	err := d.api.Do(ctx, http.MethodGet, path, nil, nil, &resp)
	if apiclient.IsStatus(err, http.StatusNotFound) {
		if _, err := d.wrangler(ctx, dir, "deploy", "--name", d.opts.Worker); err != nil {
			return fmt.Errorf("creating worker %s: %w", d.opts.Worker, err)
		}
		err = d.api.Do(ctx, http.MethodGet, path, nil, nil, &resp)
	}
	if err != nil {
		return fmt.Errorf("worker %s: %w", d.opts.Worker, err)
	}
	if resp.Result.Enabled && resp.Result.PreviewsEnabled {
		return nil
	}
	body := map[string]bool{"enabled": true, "previews_enabled": true}
	if err := d.api.Do(ctx, http.MethodPost, path, nil, body, nil); err != nil {
		return fmt.Errorf("turning on preview URLs for worker %s (or do it in the dashboard): %w", d.opts.Worker, err)
	}
	return nil
}

func (d *Deployer) accountSubdomain(ctx context.Context) (string, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.subdomain != "" {
		return d.subdomain, nil
	}
	var resp struct {
		Result struct {
			Subdomain string `json:"subdomain"`
		} `json:"result"`
	}
	if err := d.api.Do(ctx, http.MethodGet, "/accounts/"+d.opts.AccountID+"/workers/subdomain", nil, nil, &resp); err != nil {
		return "", fmt.Errorf("workers.dev subdomain: %w", err)
	}
	if resp.Result.Subdomain == "" {
		return "", errors.New("the account has no workers.dev subdomain; set one up in the dashboard")
	}
	d.subdomain = resp.Result.Subdomain
	return d.subdomain, nil
}

func (d *Deployer) wrangler(ctx context.Context, dir string, args ...string) (string, error) {
	cmd := d.opts.Wrangler
	if len(cmd) == 0 {
		cmd = []string{"npx", "--yes", "wrangler"}
	}
	vars := map[string]string{"CLOUDFLARE_API_TOKEN": d.opts.Token, "CLOUDFLARE_ACCOUNT_ID": d.opts.AccountID, "WRANGLER_SEND_METRICS": "false"}
	return run(ctx, dir, vars, cmd[0], append(slices.Clone(cmd[1:]), args...)...)
}

// run runs a command with extra environment variables and returns its
// output; on failure the error carries the end of it.
func run(ctx context.Context, dir string, vars map[string]string, name string, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Dir = dir
	cmd.Env = os.Environ()
	for _, k := range slices.Sorted(maps.Keys(vars)) {
		cmd.Env = append(cmd.Env, k+"="+vars[k])
	}
	var out bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &out
	if err := cmd.Run(); err != nil {
		return out.String(), fmt.Errorf("%s: %w\n%s", name, err, tail(out.String(), 4096))
	}
	return out.String(), nil
}

func tail(s string, n int) string {
	s = strings.TrimSpace(s)
	if len(s) > n {
		return "…" + s[len(s)-n:]
	}
	return s
}

func short(commit string) string {
	if len(commit) > 10 {
		return commit[:10]
	}
	return commit
}

// Check verifies the token and account, that the account has a workers.dev
// subdomain for preview URLs, and that the build tools are installed.
func (d *Deployer) Check(ctx context.Context) error {
	var tools []string
	if len(d.opts.Wrangler) > 0 {
		tools = append(tools, d.opts.Wrangler[0])
	} else {
		tools = append(tools, "node", "npx")
	}
	if d.opts.Build != "" {
		tools = append(tools, "sh")
	}
	errs := []error{core.NeedTools(tools...)}
	if d.opts.Worker == "" {
		errs = append(errs, errors.New("Worker is required"))
	}
	if _, err := d.accountSubdomain(ctx); err != nil {
		errs = append(errs, err)
	}
	return errors.Join(errs...)
}
