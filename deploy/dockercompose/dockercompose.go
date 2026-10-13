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
// credentials of its own. With TLS, it needs either an Issuer, with the
// credentials that issuer documents (cert/cloudflare for Cloudflare's
// "Full (strict)" mode), or one certificate and key shared by every
// environment: for Cloudflare, an Origin CA certificate (the zone → SSL/TLS
// → Origin Server → "Create Certificate"; the key is shown only once).
// Whatever the app itself needs to run (API keys, OAuth clients) goes into
// EnvFile or Env; list it from the app's .env.example.
package dockercompose

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"maps"
	"net/url"
	"os"
	"path"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/Tight-Line/hinziel/cert"
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
//
// The certificate comes from Issuer, or from CertFile and KeyFile. With an
// Issuer, each environment gets a key of its own, generated here and kept
// only on the environment's host, and a certificate for exactly its
// Hostnames. Later deploys reuse them until a third of the certificate's
// lifetime is left, and teardown revokes it. With CertFile and KeyFile,
// every environment gets a copy of the same key.
type TLS struct {
	Issuer cert.Issuer
	// Hostnames the certificate is for; nil = the hosts of every endpoint.
	// See EndpointHosts.
	Hostnames func(e *env.Environment) []string
	CertFile  string // PEM certificate (chain) on the Hinziel host
	KeyFile   string // PEM private key on the Hinziel host
	Upstream  string // "service:port" of the stack to proxy to, e.g. "nginx:80"
	// Network is the compose network Upstream is on; "" = the project's
	// default network.
	Network string
	Port    int // published port; 0 = 443
	// ReturnCookie makes the proxy name the host a login started on, for a
	// shared OAuth callback (deploy/cfoauthrelay), when the app can't.
	ReturnCookie *ReturnCookie
}

// ReturnCookie is set on responses to the paths that start a login: a
// cookie on the parent Domain holding the request's host, for ten minutes,
// so an OAuth relay on another host of that domain knows where to send the
// browser back. Apps that can set it themselves should; this is for those
// that can't be changed.
type ReturnCookie struct {
	Name   string   // "" = "oauth_return", as deploy/cfoauthrelay expects
	Domain string   // the parent domain, e.g. ".example.com"
	Paths  []string // exact paths that start a login, e.g. "/auth/google"
}

// EndpointHosts returns the hosts of the named endpoints, for TLS.Hostnames.
func EndpointHosts(names ...string) func(e *env.Environment) []string {
	return func(e *env.Environment) []string {
		var hosts []string
		for _, n := range names {
			if u, err := url.Parse(e.Endpoints[n]); err == nil && u.Hostname() != "" {
				hosts = append(hosts, u.Hostname())
			}
		}
		return hosts
	}
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
	_ deploy.Deployer  = (*Deployer)(nil)
	_ deploy.Destroyer = (*Deployer)(nil)
	_ core.Checker     = (*Deployer)(nil)
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
		f, err := d.writeTLS(ctx, h, e, genDir)
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

func (d *Deployer) writeTLS(ctx context.Context, h *remote.Host, e *env.Environment, genDir string) (string, error) {
	t := d.opts.TLS
	dir := genDir + "/tls"
	crt, key, err := d.certificate(ctx, h, e, dir)
	if err != nil {
		return "", err
	}
	for name, data := range map[string][]byte{"cert.pem": crt, "key.pem": key, "nginx.conf": []byte(tlsConf(t))} {
		if err := h.WriteFile(ctx, dir+"/"+name, data, 0o600); err != nil {
			return "", err
		}
	}
	f := genDir + "/tls.yaml"
	return f, h.WriteFile(ctx, f, []byte(tlsCompose(t, dir)), 0o644)
}

// certificate returns the certificate and key for e: the shared files, or
// with an Issuer, the ones already on the host if they're current, or else
// a new key and certificate.
func (d *Deployer) certificate(ctx context.Context, h *remote.Host, e *env.Environment, dir string) (crt, key []byte, err error) {
	t := d.opts.TLS
	if t.Issuer == nil {
		if crt, err = os.ReadFile(t.CertFile); err != nil {
			return nil, nil, fmt.Errorf("TLS certificate: %w", err)
		}
		if key, err = os.ReadFile(t.KeyFile); err != nil {
			return nil, nil, fmt.Errorf("TLS key: %w", err)
		}
		return crt, key, nil
	}
	names := d.hostnames(e)
	if len(names) == 0 {
		return nil, nil, fmt.Errorf("environment %s has no hostnames for a TLS certificate", e.Name)
	}
	// A missing file reads as empty, which is never current.
	read := func(f string) []byte {
		out, _ := h.Output(ctx, "cat "+remote.Quote(dir+"/"+f)+" 2>/dev/null || true")
		return []byte(out)
	}
	if crt, key = read("cert.pem"), read("key.pem"); cert.Current(crt, key, names, time.Now()) {
		return crt, key, nil
	}
	key, csr, err := cert.NewRequest(names)
	if err != nil {
		return nil, nil, err
	}
	if crt, err = t.Issuer.Issue(ctx, names, csr); err != nil {
		return nil, nil, fmt.Errorf("TLS certificate: %w", err)
	}
	return crt, key, nil
}

func (d *Deployer) hostnames(e *env.Environment) []string {
	if d.opts.TLS.Hostnames != nil {
		return d.opts.TLS.Hostnames(e)
	}
	return EndpointHosts(slices.Sorted(maps.Keys(e.Endpoints))...)(e)
}

// Destroy revokes the environment's certificate, if an Issuer issued it.
// The files on the host go with the environment.
func (d *Deployer) Destroy(ctx context.Context, e *env.Environment) error {
	t := d.opts.TLS
	if t == nil || t.Issuer == nil {
		return nil
	}
	names := d.hostnames(e)
	if len(names) == 0 {
		return nil
	}
	if err := t.Issuer.Revoke(ctx, names); err != nil {
		return fmt.Errorf("revoking the TLS certificate: %w", err)
	}
	return nil
}

// tlsConf proxies HTTPS to t.Upstream. The upstream is resolved per request
// through Docker's DNS, so the proxy starts even before the upstream does.
// With a ReturnCookie, the paths that start a login also set the cookie.
func tlsConf(t *TLS) string {
	const proxy = `        proxy_pass $upstream;
        proxy_set_header Host $host;
        proxy_set_header X-Forwarded-For $proxy_add_x_forwarded_for;
        proxy_set_header X-Forwarded-Proto https;
        proxy_read_timeout 600s;
`
	var b strings.Builder
	b.WriteString(`server {
    listen 443 ssl;
    ssl_certificate /etc/nginx/hinziel/cert.pem;
    ssl_certificate_key /etc/nginx/hinziel/key.pem;
    client_max_body_size 0;
    resolver 127.0.0.11 valid=10s;
    set $upstream http://` + t.Upstream + `;
    location / {
` + proxy + `    }
`)
	if c := t.ReturnCookie; c != nil {
		name := c.Name
		if name == "" {
			name = "oauth_return"
		}
		cookie := fmt.Sprintf("%s=$host; Domain=%s; Path=/; Max-Age=600; Secure; HttpOnly; SameSite=Lax", name, c.Domain)
		for _, p := range c.Paths {
			fmt.Fprintf(&b, "    location = %s {\n        add_header Set-Cookie %q always;\n%s    }\n", p, cookie, proxy)
		}
	}
	b.WriteString("}\n")
	return b.String()
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

// Check verifies the options, that the TLS files are readable or the Issuer
// works, and that ssh is installed. Docker itself runs on the environment's
// host, not here.
func (d *Deployer) Check(ctx context.Context) error {
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
		if c := t.ReturnCookie; c != nil {
			if !strings.HasPrefix(c.Domain, ".") || len(c.Paths) == 0 {
				errs = append(errs, errors.New("TLS.ReturnCookie needs a parent Domain starting with a dot and at least one path"))
			}
			for _, p := range c.Paths {
				if !strings.HasPrefix(p, "/") || strings.ContainsAny(p, " ;{}\"") {
					errs = append(errs, fmt.Errorf("TLS.ReturnCookie: %q is not a plain path", p))
				}
			}
		}
		switch {
		case t.Issuer != nil && (t.CertFile != "" || t.KeyFile != ""):
			errs = append(errs, errors.New("TLS takes an Issuer or CertFile and KeyFile, not both"))
		case t.Issuer != nil:
			if c, ok := t.Issuer.(core.Checker); ok {
				if err := c.Check(ctx); err != nil {
					errs = append(errs, fmt.Errorf("TLS: %w", err))
				}
			}
		default:
			for _, f := range []string{t.CertFile, t.KeyFile} {
				if _, err := os.Stat(f); err != nil {
					errs = append(errs, fmt.Errorf("TLS: %w", err))
				}
			}
		}
	}
	return errors.Join(errs...)
}
