package cfworkers

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/Tight-Line/skrat/deploy"
	"github.com/Tight-Line/skrat/deploy/deploytest"
	"github.com/Tight-Line/skrat/env"
)

// fakeAPI serves the Workers subdomain endpoints for account "acct".
type fakeAPI struct {
	mu       sync.Mutex
	workers  map[string]map[string]bool // worker -> enabled, previews_enabled
	enabling int
}

func (f *fakeAPI) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if r.Header.Get("Authorization") != "Bearer cf-token" {
		http.Error(w, `{"success":false}`, http.StatusForbidden)
		return
	}
	ok := func(v any) { json.NewEncoder(w).Encode(map[string]any{"success": true, "result": v}) }
	parts := strings.Split(strings.Trim(r.URL.Path, "/"), "/")
	switch {
	case r.URL.Path == "/accounts/acct/workers/subdomain":
		ok(map[string]string{"subdomain": "acme"})
	case len(parts) == 6 && parts[3] == "scripts" && parts[5] == "subdomain":
		s, exists := f.workers[parts[4]]
		if !exists {
			http.Error(w, `{"success":false,"errors":[{"code":10007,"message":"workers.api.error.script_not_found"}]}`, http.StatusNotFound)
			return
		}
		if r.Method == "POST" {
			f.enabling++
			var body map[string]bool
			json.NewDecoder(r.Body).Decode(&body)
			s["enabled"], s["previews_enabled"] = body["enabled"], body["previews_enabled"]
		}
		ok(s)
	default:
		http.NotFound(w, r)
	}
}

// fakeWrangler logs its calls and, on deploy, tells the fake API the worker
// now exists (by touching a file the test turns into API state).
func setup(t *testing.T, aliasInOutput bool) (*Deployer, *fakeAPI, *env.Environment, *deploy.Source, string) {
	t.Helper()
	api := &fakeAPI{workers: map[string]map[string]bool{}}
	srv := httptest.NewServer(api)
	t.Cleanup(srv.Close)

	dir := t.TempDir()
	log := filepath.Join(dir, "wrangler.log")
	created := filepath.Join(dir, "created")
	out := "Worker Version ID: 0a1b2c3d-0000-1111-2222-333344445555\n"
	if aliasInOutput {
		out += "Version Preview URL: https://0a1b2c3d-previews.acme.workers.dev\n" +
			"Version Preview Alias URL: https://sk-abc-previews.acme.workers.dev\n"
	}
	script := "#!/bin/sh\necho \"$PWD|$CLOUDFLARE_ACCOUNT_ID|$CLOUDFLARE_API_TOKEN|$*\" >> " + log + "\n" +
		"[ \"$1\" = deploy ] && touch " + created + "\n" +
		"[ \"$1\" = versions ] && [ ! -f dist/index.html ] && { echo 'no assets' >&2; exit 1; }\n" +
		"printf '%s' '" + out + "'\n"
	wrangler := filepath.Join(dir, "wrangler")
	os.WriteFile(wrangler, []byte(script), 0o755)

	src := t.TempDir()
	os.WriteFile(filepath.Join(src, "wrangler.jsonc"), []byte(`{"name":"production-worker"}`), 0o644)

	d := New(Options{
		AccountID: "acct", Token: "cf-token", Worker: "previews", BaseURL: srv.URL,
		Build: `mkdir -p dist && echo "$VITE_API_URL" > dist/index.html`,
		BuildEnv: func(e *env.Environment, src *deploy.Source) map[string]string {
			return map[string]string{"VITE_API_URL": e.Endpoints["api"]}
		},
		Wrangler: []string{wrangler},
	})
	// The fake API learns about workers the fake wrangler created.
	d.api.HTTP = &http.Client{Transport: roundTripper(func(r *http.Request) (*http.Response, error) {
		if _, err := os.Stat(created); err == nil {
			api.mu.Lock()
			if api.workers["previews"] == nil {
				api.workers["previews"] = map[string]bool{"enabled": true}
			}
			api.mu.Unlock()
		}
		return http.DefaultTransport.RoundTrip(r)
	})}
	e := &env.Environment{Name: "sk-abc", Endpoints: map[string]string{"api": "https://sk-abc-api.example.com"}}
	return d, api, e, &deploy.Source{Repo: "app", Workdir: src, Branch: "skrat/abc", Commit: "0123456789abcdef"}, log
}

type roundTripper func(*http.Request) (*http.Response, error)

func (f roundTripper) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestConformance(t *testing.T) {
	d, _, e, src, _ := setup(t, true)
	deploytest.Run(t, d, e, src)
}

func TestDeploy(t *testing.T) {
	d, api, e, src, log := setup(t, true)
	dep, err := d.Deploy(context.Background(), e, src)
	if err != nil {
		t.Fatal(err)
	}
	if dep.URL != "https://sk-abc-previews.acme.workers.dev" || dep.Info["version_id"] != "0a1b2c3d-0000-1111-2222-333344445555" {
		t.Errorf("deployment %+v", dep)
	}
	if html, _ := os.ReadFile(filepath.Join(src.Workdir, "dist", "index.html")); string(html) != "https://sk-abc-api.example.com\n" {
		t.Errorf("BuildEnv must reach the build: %q", html)
	}
	data, _ := os.ReadFile(log)
	calls := strings.Split(strings.TrimSpace(string(data)), "\n")
	if len(calls) != 2 {
		t.Fatalf("wrangler calls:\n%s", data)
	}
	if want := src.Workdir + "|acct|cf-token|deploy --name previews"; calls[0] != want {
		t.Errorf("first use must create the worker:\n%s\nwant\n%s", calls[0], want)
	}
	if want := "versions upload --name previews --preview-alias sk-abc --message app 0123456789 (skrat/abc)"; !strings.HasSuffix(calls[1], want) {
		t.Errorf("upload:\n%s", calls[1])
	}
	if !api.workers["previews"]["previews_enabled"] || api.enabling != 1 {
		t.Errorf("preview URLs must be turned on once: %+v, %d", api.workers, api.enabling)
	}

	// A second deploy finds the worker and doesn't create it again.
	if _, err := d.Deploy(context.Background(), e, src); err != nil {
		t.Fatal(err)
	}
	data, _ = os.ReadFile(log)
	if n := strings.Count(string(data), "|deploy "); n != 1 {
		t.Errorf("worker created %d times", n)
	}
}

func TestURLFallsBackToSubdomain(t *testing.T) {
	d, _, e, src, _ := setup(t, false)
	dep, err := d.Deploy(context.Background(), e, src)
	if err != nil {
		t.Fatal(err)
	}
	if dep.URL != PreviewURL("sk-abc", "previews", "acme") {
		t.Errorf("URL %s", dep.URL)
	}
}

func TestAliasValidation(t *testing.T) {
	d := New(Options{Worker: strings.Repeat("w", 50)})
	for _, name := range []string{"Sk-ABC", "1abc", "sk_abc", "sk-" + strings.Repeat("x", 20)} {
		if _, err := d.alias(&env.Environment{Name: name}); err == nil {
			t.Errorf("alias %q must be refused", name)
		}
	}
}

func TestBuildFailure(t *testing.T) {
	d, _, e, src, _ := setup(t, true)
	d.opts.Build = "echo 'vite: cannot find module' >&2; exit 1"
	if _, err := d.Deploy(context.Background(), e, src); err == nil || !strings.Contains(err.Error(), "cannot find module") {
		t.Errorf("a build failure must carry its output: %v", err)
	}
}

func TestCheck(t *testing.T) {
	d, _, _, _, _ := setup(t, true)
	if err := d.Check(context.Background()); err != nil {
		t.Fatal(err)
	}
	bad := New(Options{AccountID: "acct", Token: "wrong", Worker: "previews", BaseURL: d.api.BaseURL})
	if err := bad.Check(context.Background()); err == nil {
		t.Error("a bad token must fail the check")
	}
}
