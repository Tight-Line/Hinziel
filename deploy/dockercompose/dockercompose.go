// Package dockercompose is a deploy.Deployer that runs a repository's own
// Docker Compose files on an environment's host over SSH.
//
// It takes the app as it is: the team's compose files are used unchanged,
// and everything specific to previews is layered on top of them: an .env
// file, an extra compose file (Override), a TLS proxy in front of the stack,
// and commands to run after each deploy (migrations, cache warm-up).
//
// On the host, each deploy uploads the commit to <RemoteDir>/src (replacing
// the previous one) and writes Hinziel's own files to <RemoteDir>/hinziel,
// outside the build context, so a `COPY . .` never bakes them into an image.
// The host needs Docker with the compose plugin.
//
// # Credentials
//
// It reaches the host with the environment's SSH access, so it needs no
// credentials of its own. With TLS, it needs the certificate and key files:
// for Cloudflare's "Full (strict)" mode, a Cloudflare Origin CA certificate
// (the zone → SSL/TLS → Origin Server → "Create Certificate"; the key is
// shown only once). Whatever the app itself needs to run (API keys, OAuth
// clients) goes into EnvFile or Env; list it from the app's .env.example.
package dockercompose

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"maps"
	"os"
	"path"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/Tight-Line/hinziel/core"
	"github.com/Tight-Line/hinziel/core/remote"
	"github.com/Tight-Line/hinziel/deploy"
	"github.com/Tight-Line/hinziel/env"
)

// Options configure a compose deployment.
type Options struct {
	// Dir is the directory in the repository holding the compose files;
	// "" = the repository root. Compose runs from here.
	Dir string
	// Files are the compose files, relative to Dir; nil = compose's default
	// (compose.yaml or docker-compose.yml).
	Files []string
	// Override is compose YAML applied after Files, for what previews need
	// differently: a service to drop, a port to publish. Relative paths in
	// it resolve against Dir.
	Override string
	// EnvFile is the content of the .env file in Dir, which compose and
	// most apps read. Env is appended to it and wins over it.
	EnvFile string
	Env     func(e *env.Environment) map[string]string
	// EnvFileName is the name of the env file in Dir; "" = ".env".
	EnvFileName string
	// TLS adds a TLS-terminating proxy in front of the stack.
	TLS *TLS
	// PostDeploy runs, in order, in running services after every deploy.
	PostDeploy []Exec
	// Project is the compose project name; nil = "<environment>-<repo>".
	Project func(e *env.Environment) string
	// Host is the index of the environment host to deploy to.
	Host int
	// RemoteDir is where the deployment lives on the host;
	// nil = /srv/hinziel/<environment>/<repo>.
	RemoteDir func(e *env.Environment, repo string) string
	// URL, if set, is the deployment's URL (deploy.Deployment.URL).
	URL func(e *env.Environment) string
}

// TLS terminates HTTPS on the host and proxies to a service of the stack.
// With Cloudflare proxying in "Full (strict)" mode, a Cloudflare Origin CA
// certificate is enough.
type TLS struct {
	CertFile string // PEM certificate (chain) on the Hinziel host
	KeyFile  string // PEM private key on the Hinziel host
	Upstream string // "service:port" of the stack to proxy to, e.g. "nginx:80"
	// Network is the compose network Upstream is on; "" = the project's
	// default network.
	Network string
	Port    int // published port; 0 = 443
}

// Exec is a command run with `docker compose exec` in a running service.
type Exec struct {
	Service string
	Command string // run with sh -c
}

// Deployer deploys with Docker Compose.
type Deployer struct {
	opts Options
	ssh  []string // tests replace the ssh command
}

var (
	_ deploy.Deployer = (*Deployer)(nil)
	_ core.Checker    = (*Deployer)(nil)
)

// New returns a compose deployer.
func New(opts Options) *Deployer { return &Deployer{opts: opts} }

// Deploy uploads src to the host and brings the stack up.
func (d *Deployer) Deploy(ctx context.Context, e *env.Environment, src *deploy.Source) (*deploy.Deployment, error) {
	if d.opts.Host >= len(e.Hosts) {
		return nil, fmt.Errorf("environment %s has no host %d", e.Name, d.opts.Host)
	}
	h, err := remote.FromEnv(e.Hosts[d.opts.Host])
	if err != nil {
		return nil, err
	}
	h.SSH = d.ssh

	base := "/srv/hinziel/" + e.Name + "/" + src.Repo
	if d.opts.RemoteDir != nil {
		base = d.opts.RemoteDir(e, src.Repo)
	}
	srcDir, genDir := base+"/src", base+"/hinziel"
	workDir := path.Join(srcDir, d.opts.Dir)

	if err := h.Upload(ctx, src.Workdir, srcDir, ".git"); err != nil {
		return nil, fmt.Errorf("uploading %s: %w", src.Repo, err)
	}
	if err := h.WriteFile(ctx, path.Join(workDir, d.envFileName()), []byte(d.envFile(e)), 0o600); err != nil {
		return nil, err
	}
	files := slices.Clone(d.opts.Files)
	if d.opts.Override != "" {
		f := genDir + "/override.yaml"
		if err := h.WriteFile(ctx, f, []byte(d.opts.Override), 0o644); err != nil {
			return nil, err
		}
		files = append(files, f)
	}
	if d.opts.TLS != nil {
		f, err := d.writeTLS(ctx, h, genDir)
		if err != nil {
			return nil, err
		}
		files = append(files, f)
	}

	compose := "docker compose -p " + remote.Quote(d.project(e, src.Repo))
	for _, f := range files {
		compose += " -f " + remote.Quote(f)
	}
	// --force-recreate: the source tree was just replaced, and a container
	// that isn't recreated keeps bind mounts into the deleted one.
	script := fmt.Sprintf("set -e; cd %s; %s up -d --build --force-recreate --remove-orphans",
		remote.Quote(workDir), compose)
	for _, x := range d.opts.PostDeploy {
		script += fmt.Sprintf("; %s exec -T %s sh -c %s", compose, remote.Quote(x.Service), remote.Quote(x.Command))
	}
	if err := h.Run(ctx, script, nil, nil); err != nil {
		return nil, fmt.Errorf("docker compose: %w", err)
	}

	dep := &deploy.Deployment{Commit: src.Commit, At: time.Now(), Info: map[string]string{"host": h.Address, "dir": workDir}}
	if d.opts.URL != nil {
		dep.URL = d.opts.URL(e)
	}
	return dep, nil
}

func (d *Deployer) envFileName() string {
	if d.opts.EnvFileName != "" {
		return d.opts.EnvFileName
	}
	return ".env"
}

func (d *Deployer) envFile(e *env.Environment) string {
	var b strings.Builder
	b.WriteString(strings.TrimRight(d.opts.EnvFile, "\n"))
	if d.opts.Env != nil {
		vars := d.opts.Env(e)
		if len(vars) > 0 {
			b.WriteString("\n\n# Set by Hinziel for environment " + e.Name + "\n")
		}
		for _, k := range slices.Sorted(maps.Keys(vars)) {
			b.WriteString(k + "=" + quoteEnv(vars[k]) + "\n")
		}
	}
	return strings.TrimLeft(b.String(), "\n")
}

var plainEnvValue = regexp.MustCompile(`^[A-Za-z0-9_./:@,+=-]*$`)

// quoteEnv quotes a value for a dotenv file. Single quotes are literal for
// both compose and the common dotenv libraries (no escapes, no $ expansion),
// so they're used whenever the value allows; otherwise double quotes, where
// a "$" may be expanded.
func quoteEnv(v string) string {
	switch {
	case plainEnvValue.MatchString(v):
		return v
	case !strings.ContainsAny(v, "'\n"):
		return "'" + v + "'"
	}
	r := strings.NewReplacer(`\`, `\\`, `"`, `\"`, "\n", `\n`)
	return `"` + r.Replace(v) + `"`
}

var projectUnsafe = regexp.MustCompile(`[^a-z0-9_-]+`)

func (d *Deployer) project(e *env.Environment, repo string) string {
	if d.opts.Project != nil {
		return d.opts.Project(e)
	}
	return strings.Trim(projectUnsafe.ReplaceAllString(strings.ToLower(e.Name+"-"+repo), "-"), "-")
}

func (d *Deployer) writeTLS(ctx context.Context, h *remote.Host, genDir string) (string, error) {
	t := d.opts.TLS
	cert, err := os.ReadFile(t.CertFile)
	if err != nil {
		return "", fmt.Errorf("TLS certificate: %w", err)
	}
	key, err := os.ReadFile(t.KeyFile)
	if err != nil {
		return "", fmt.Errorf("TLS key: %w", err)
	}
	dir := genDir + "/tls"
	for name, data := range map[string][]byte{"cert.pem": cert, "key.pem": key, "nginx.conf": []byte(tlsConf(t.Upstream))} {
		if err := h.WriteFile(ctx, dir+"/"+name, data, 0o600); err != nil {
			return "", err
		}
	}
	f := genDir + "/tls.yaml"
	return f, h.WriteFile(ctx, f, []byte(tlsCompose(t, dir)), 0o644)
}

// tlsConf proxies HTTPS to upstream. The upstream is resolved per request
// through Docker's DNS, so the proxy starts even before the upstream does.
func tlsConf(upstream string) string {
	return `server {
    listen 443 ssl;
    ssl_certificate /etc/nginx/hinziel/cert.pem;
    ssl_certificate_key /etc/nginx/hinziel/key.pem;
    client_max_body_size 0;
    resolver 127.0.0.11 valid=10s;
    set $upstream http://` + upstream + `;
    location / {
        proxy_pass $upstream;
        proxy_set_header Host $host;
        proxy_set_header X-Forwarded-For $proxy_add_x_forwarded_for;
        proxy_set_header X-Forwarded-Proto https;
        proxy_read_timeout 600s;
    }
}
`
}

func tlsCompose(t *TLS, dir string) string {
	port := t.Port
	if port == 0 {
		port = 443
	}
	var b bytes.Buffer
	fmt.Fprintf(&b, `services:
  hinziel-tls:
    image: nginx:1.27-alpine
    restart: unless-stopped
    ports:
      - "%d:443"
    volumes:
      - %s/nginx.conf:/etc/nginx/conf.d/default.conf:ro
      - %s:/etc/nginx/hinziel:ro
`, port, dir, dir)
	if t.Network != "" {
		fmt.Fprintf(&b, "    networks:\n      - %s\n", t.Network)
	}
	return b.String()
}

// Check verifies the options, that the TLS files are readable and that ssh
// is installed. Docker itself runs on the environment's host, not here.
func (d *Deployer) Check(context.Context) error {
	ssh := "ssh"
	if len(d.ssh) > 0 {
		ssh = d.ssh[0]
	}
	errs := []error{core.NeedTools(ssh)}
	for i, x := range d.opts.PostDeploy {
		if x.Service == "" || x.Command == "" {
			errs = append(errs, fmt.Errorf("PostDeploy[%d] needs a Service and a Command", i))
		}
	}
	if t := d.opts.TLS; t != nil {
		if t.Upstream == "" {
			errs = append(errs, errors.New("TLS needs an Upstream"))
		}
		for _, f := range []string{t.CertFile, t.KeyFile} {
			if _, err := os.Stat(f); err != nil {
				errs = append(errs, fmt.Errorf("TLS: %w", err))
			}
		}
	}
	return errors.Join(errs...)
}
