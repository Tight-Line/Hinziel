package dockercompose

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Tight-Line/hinziel/cert"
	"github.com/Tight-Line/hinziel/cert/certtest"
	"github.com/Tight-Line/hinziel/core/remote/remotetest"
	"github.com/Tight-Line/hinziel/deploy"
	"github.com/Tight-Line/hinziel/deploy/deploytest"
	"github.com/Tight-Line/hinziel/env"
)

// setup returns an environment whose host is this machine (through a local
// ssh), a source checkout and the path of a log of every docker call.
func setup(t *testing.T) (*env.Environment, *deploy.Source, string) {
	t.Helper()
	bin := t.TempDir()
	log := filepath.Join(t.TempDir(), "docker.log")
	fake := "#!/bin/sh\necho \"$PWD: $*\" >> " + log + "\n"
	if err := os.WriteFile(filepath.Join(bin, "docker"), []byte(fake), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+":"+os.Getenv("PATH"))

	src := t.TempDir()
	os.WriteFile(filepath.Join(src, "vibe.docker-compose.yaml"), []byte("services: {}\n"), 0o644)
	os.MkdirAll(filepath.Join(src, ".git"), 0o755)
	os.WriteFile(filepath.Join(src, ".git", "HEAD"), []byte("x"), 0o644)

	e := &env.Environment{
		Name:      "hz-abc",
		Hosts:     []env.Host{{Name: "main", Address: "203.0.113.10", SSH: &env.SSHAccess{User: "root"}}},
		Endpoints: map[string]string{"api": "https://hz-abc-api.example.com", "web": "https://hz-abc.example.dev"},
	}
	return e, &deploy.Source{Repo: "app", Workdir: src, Branch: "hinziel/abc", Commit: "abc123"}, log
}

func newTestDeployer(t *testing.T, opts Options) (*Deployer, string) {
	root := t.TempDir()
	opts.RemoteDir = func(e *env.Environment, repo string) string { return filepath.Join(root, e.Name, repo) }
	d := New(opts)
	d.ssh = remotetest.LocalSSH(t)
	return d, filepath.Join(root, "hz-abc", "app")
}

func readFile(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

func TestConformance(t *testing.T) {
	e, src, _ := setup(t)
	d, _ := newTestDeployer(t, Options{Files: []string{"vibe.docker-compose.yaml"}})
	deploytest.Run(t, d, e, src)
}

func TestDeploy(t *testing.T) {
	e, src, log := setup(t)
	certDir := t.TempDir()
	os.WriteFile(filepath.Join(certDir, "cert.pem"), []byte("CERT"), 0o600)
	os.WriteFile(filepath.Join(certDir, "key.pem"), []byte("KEY"), 0o600)

	d, base := newTestDeployer(t, Options{
		Files:    []string{"vibe.docker-compose.yaml"},
		Override: "services:\n  cron:\n    profiles: [disabled]\n",
		EnvFile:  "APP_ENV=preview\nONSOCIAL_DISABLED=true\n",
		Env: func(e *env.Environment) map[string]string {
			return map[string]string{"APP_URL": e.Endpoints["api"], "APP_NAME": "Blank Tracker", "SECRET": `p$ss"word`, "MULTI": "a'b"}
		},
		TLS:        &TLS{CertFile: filepath.Join(certDir, "cert.pem"), KeyFile: filepath.Join(certDir, "key.pem"), Upstream: "nginx:80", Network: "blanktracker"},
		PostDeploy: []Exec{{Service: "app", Command: "php artisan migrate --force"}},
		URL:        func(e *env.Environment) string { return e.Endpoints["api"] },
	})
	dep, err := d.Deploy(context.Background(), e, src)
	if err != nil {
		t.Fatal(err)
	}
	if dep.URL != "https://hz-abc-api.example.com" || dep.Commit != "abc123" {
		t.Errorf("deployment %+v", dep)
	}

	srcDir := filepath.Join(base, "src")
	if _, err := os.Stat(filepath.Join(srcDir, ".git")); !os.IsNotExist(err) {
		t.Error(".git must not be uploaded")
	}
	wantEnv := "APP_ENV=preview\nONSOCIAL_DISABLED=true\n\n# Set by Hinziel for environment hz-abc\n" +
		"APP_NAME='Blank Tracker'\nAPP_URL=https://hz-abc-api.example.com\nMULTI=\"a'b\"\nSECRET='p$ss\"word'\n"
	if got := readFile(t, filepath.Join(srcDir, ".env")); got != wantEnv {
		t.Errorf(".env\n%s\nwant\n%s", got, wantEnv)
	}
	gen := filepath.Join(base, "hinziel")
	if got := readFile(t, filepath.Join(gen, "override.yaml")); !strings.Contains(got, "profiles: [disabled]") {
		t.Errorf("override %q", got)
	}
	if readFile(t, filepath.Join(gen, "tls", "key.pem")) != "KEY" {
		t.Error("TLS key not written")
	}
	tlsYAML := readFile(t, filepath.Join(gen, "tls.yaml"))
	if !strings.Contains(tlsYAML, `"443:443"`) || !strings.Contains(tlsYAML, "- blanktracker") {
		t.Errorf("tls.yaml\n%s", tlsYAML)
	}
	if conf := readFile(t, filepath.Join(gen, "tls", "nginx.conf")); !strings.Contains(conf, "set $upstream http://nginx:80;") {
		t.Errorf("nginx.conf\n%s", conf)
	}

	calls := strings.Split(strings.TrimSpace(readFile(t, log)), "\n")
	if len(calls) != 2 {
		t.Fatalf("docker calls:\n%s", strings.Join(calls, "\n"))
	}
	files := "-f vibe.docker-compose.yaml -f " + gen + "/override.yaml -f " + gen + "/tls.yaml"
	if want := srcDir + ": compose -p hz-abc-app " + files + " up -d --build --force-recreate --remove-orphans"; calls[0] != want {
		t.Errorf("up:\n%s\nwant\n%s", calls[0], want)
	}
	if want := "compose -p hz-abc-app " + files + " exec -T app sh -c php artisan migrate --force"; !strings.HasSuffix(calls[1], want) {
		t.Errorf("post-deploy:\n%s", calls[1])
	}
}

func TestDeployIssuesACertificatePerEnvironment(t *testing.T) {
	e, src, _ := setup(t)
	e.Endpoints["nova"] = "https://hz-abc-nova.example.com/nova"
	ca := certtest.NewFake()
	names := []string{"hz-abc-api.example.com", "hz-abc-nova.example.com"}
	d, base := newTestDeployer(t, Options{TLS: &TLS{Issuer: ca, Hostnames: EndpointHosts("api", "nova", "missing"), Upstream: "nginx:80"}})
	tlsDir := filepath.Join(base, "hinziel", "tls")
	pair := func() (string, string) {
		return readFile(t, filepath.Join(tlsDir, "cert.pem")), readFile(t, filepath.Join(tlsDir, "key.pem"))
	}
	ctx := context.Background()

	if _, err := d.Deploy(ctx, e, src); err != nil {
		t.Fatal(err)
	}
	crt, key := pair()
	if !cert.Current([]byte(crt), []byte(key), names, time.Now()) {
		t.Fatalf("the certificate on the host isn't for %v", names)
	}

	// A redeploy reuses the key and certificate.
	if _, err := d.Deploy(ctx, e, src); err != nil {
		t.Fatal(err)
	}
	if c, k := pair(); c != crt || k != key || ca.Revoked() != 0 {
		t.Error("a redeploy must reuse a current certificate")
	}

	// A host without its key (a recreated droplet) gets a new key, and the
	// old certificate is revoked.
	os.Remove(filepath.Join(tlsDir, "key.pem"))
	if _, err := d.Deploy(ctx, e, src); err != nil {
		t.Fatal(err)
	}
	if _, k := pair(); k == key || ca.Revoked() != 1 || ca.Current(names) != 1 {
		t.Errorf("a lost key must be replaced: revoked %d, current %d", ca.Revoked(), ca.Current(names))
	}

	if err := d.Destroy(ctx, e); err != nil {
		t.Fatal(err)
	}
	if ca.Current(names) != 0 {
		t.Error("teardown must revoke the certificate")
	}

	ca.IssueErrs = []error{errors.New("CA down")}
	os.Remove(filepath.Join(tlsDir, "key.pem"))
	if _, err := d.Deploy(ctx, e, src); err == nil || !strings.Contains(err.Error(), "CA down") {
		t.Errorf("a failed issue must fail the deploy: %v", err)
	}
}

func TestDestroyWithoutIssuer(t *testing.T) {
	e, _, _ := setup(t)
	if err := New(Options{}).Destroy(context.Background(), e); err != nil {
		t.Error(err)
	}
}

func TestDeployFailureCarriesOutput(t *testing.T) {
	e, src, _ := setup(t)
	bin := t.TempDir()
	os.WriteFile(filepath.Join(bin, "docker"), []byte("#!/bin/sh\necho 'service app: build failed' >&2\nexit 1\n"), 0o755)
	t.Setenv("PATH", bin+":"+os.Getenv("PATH"))
	d, _ := newTestDeployer(t, Options{})
	_, err := d.Deploy(context.Background(), e, src)
	if err == nil || !strings.Contains(err.Error(), "build failed") {
		t.Fatalf("error must carry compose's output: %v", err)
	}
}

func TestCheck(t *testing.T) {
	d := New(Options{TLS: &TLS{CertFile: "/nope/cert.pem", KeyFile: "/nope/key.pem"}, PostDeploy: []Exec{{Service: "app"}}})
	err := d.Check(context.Background())
	for _, want := range []string{"PostDeploy[0]", "Upstream", "cert.pem", "key.pem"} {
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("check must report %s: %v", want, err)
		}
	}
	if err := New(Options{}).Check(context.Background()); err != nil {
		t.Error(err)
	}
	both := New(Options{TLS: &TLS{Issuer: certtest.NewFake(), CertFile: "/nope/cert.pem", Upstream: "nginx:80"}})
	if err := both.Check(context.Background()); err == nil || !strings.Contains(err.Error(), "not both") {
		t.Errorf("an Issuer and CertFile together must fail the check: %v", err)
	}
	issued := New(Options{TLS: &TLS{Issuer: certtest.NewFake(), Upstream: "nginx:80"}})
	if err := issued.Check(context.Background()); err != nil {
		t.Errorf("an Issuer needs no files: %v", err)
	}
}

// On a host with nothing installed, Check names the tools it needs.
func TestCheckNamesMissingTools(t *testing.T) {
	t.Setenv("PATH", t.TempDir())
	err := New(Options{}).Check(context.Background())
	if err == nil || !strings.Contains(err.Error(), "not installed on this host (or not on PATH): ssh") {
		t.Errorf("want missing ssh, got %v", err)
	}
}

func TestTLSReturnCookie(t *testing.T) {
	tls := &TLS{Upstream: "nginx:80", ReturnCookie: &ReturnCookie{Domain: ".example.com", Paths: []string{"/auth/google", "/api/auth/google"}}}
	conf := tlsConf(tls)
	for _, want := range []string{
		"location = /auth/google {",
		"location = /api/auth/google {",
		`add_header Set-Cookie "oauth_return=$host; Domain=.example.com; Path=/; Max-Age=600; Secure; HttpOnly; SameSite=Lax" always;`,
		"location / {",
	} {
		if !strings.Contains(conf, want) {
			t.Errorf("config lacks %q:\n%s", want, conf)
		}
	}
	if strings.Count(conf, "proxy_pass $upstream;") != 3 {
		t.Errorf("every location must proxy:\n%s", conf)
	}
	if strings.Contains(tlsConf(&TLS{Upstream: "nginx:80"}), "Set-Cookie") {
		t.Error("no cookie without ReturnCookie")
	}
	if os.Getenv("HZ_DUMP_NGINX_CONF") != "" {
		os.WriteFile(os.Getenv("HZ_DUMP_NGINX_CONF"), []byte(conf), 0o644)
	}

	certDir := t.TempDir()
	os.WriteFile(filepath.Join(certDir, "c"), []byte("x"), 0o600)
	for _, bad := range []*ReturnCookie{
		{Domain: "example.com", Paths: []string{"/auth/google"}},
		{Domain: ".example.com"},
		{Domain: ".example.com", Paths: []string{"/a; return 200"}},
	} {
		d := New(Options{TLS: &TLS{CertFile: filepath.Join(certDir, "c"), KeyFile: filepath.Join(certDir, "c"), Upstream: "nginx:80", ReturnCookie: bad}})
		if err := d.Check(context.Background()); err == nil || !strings.Contains(err.Error(), "ReturnCookie") {
			t.Errorf("%+v: got %v", bad, err)
		}
	}
}
