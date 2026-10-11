package cfoauthrelay

import (
	"context"
	"encoding/json"
	"io"
	"mime"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

// fakeCF is the slice of the Cloudflare API the relay uses.
type fakeCF struct {
	mu      sync.Mutex
	scripts map[string]string // name -> module source
	routes  []route
	records []map[string]any
	calls   []string
}

func (f *fakeCF) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, r.Method+" "+r.URL.Path)
	auth := r.Header.Get("Authorization")
	send := func(v any) { json.NewEncoder(w).Encode(map[string]any{"success": true, "result": v}) }
	switch p := r.URL.Path; {
	case p == "/zones" && auth == "Bearer dns":
		send([]map[string]string{{"id": "z1"}})
	case p == "/zones/z1/dns_records" && auth == "Bearer dns" && r.Method == "GET":
		var out []map[string]any
		for _, rec := range f.records {
			if rec["name"] == r.URL.Query().Get("name") {
				out = append(out, rec)
			}
		}
		send(out)
	case p == "/zones/z1/dns_records" && auth == "Bearer dns" && r.Method == "POST":
		var rec map[string]any
		json.NewDecoder(r.Body).Decode(&rec)
		f.records = append(f.records, rec)
		send(rec)
	case p == "/zones/z1/workers/routes" && auth == "Bearer work" && r.Method == "GET":
		send(f.routes)
	case p == "/zones/z1/workers/routes" && auth == "Bearer work" && r.Method == "POST":
		var rt route
		json.NewDecoder(r.Body).Decode(&rt)
		rt.ID = "r1"
		f.routes = append(f.routes, rt)
		send(rt)
	case strings.HasPrefix(p, "/accounts/acc/workers/scripts/") && auth == "Bearer work" && r.Method == "PUT":
		_, params, _ := mime.ParseMediaType(r.Header.Get("Content-Type"))
		mr := multipart.NewReader(r.Body, params["boundary"])
		var meta map[string]string
		var source string
		for {
			part, err := mr.NextPart()
			if err != nil {
				break
			}
			b, _ := io.ReadAll(part)
			switch part.FormName() {
			case "metadata":
				json.Unmarshal(b, &meta)
			case meta["main_module"]:
				source = string(b)
			}
		}
		if source == "" {
			http.Error(w, `{"success":false,"errors":[{"message":"no main module"}]}`, http.StatusBadRequest)
			return
		}
		f.scripts[strings.TrimPrefix(p, "/accounts/acc/workers/scripts/")] = source
		send(map[string]string{"id": "hinziel-oauth-relay"})
	case p == "/accounts/acc/workers/subdomain" && auth == "Bearer work":
		send(map[string]string{"subdomain": "acct"})
	default:
		http.Error(w, `{"success":false}`, http.StatusForbidden)
	}
}

func newTest(t *testing.T) (*Deployer, *fakeCF) {
	f := &fakeCF{scripts: map[string]string{}}
	srv := httptest.NewServer(f)
	t.Cleanup(srv.Close)
	return New(Options{
		AccountID: "acc", WorkersToken: "work", DNSToken: "dns", BaseURL: srv.URL,
		Zone: "example.com", Host: "hz-auth.example.com",
	}), f
}

func TestDeployKeepsTheRelayInPlace(t *testing.T) {
	d, f := newTest(t)
	for range 2 { // the second deploy must change nothing but the script
		dep, err := d.Deploy(context.Background(), nil, nil)
		if err != nil {
			t.Fatal(err)
		}
		if dep.URL != "https://hz-auth.example.com" {
			t.Errorf("URL %s", dep.URL)
		}
	}
	if src := f.scripts["hinziel-oauth-relay"]; !strings.Contains(src, `const ALLOWED_SUFFIX = ".example.com";`) {
		t.Errorf("uploaded script:\n%s", src)
	}
	if len(f.routes) != 1 || f.routes[0].Pattern != "hz-auth.example.com/*" || f.routes[0].Script != "hinziel-oauth-relay" {
		t.Errorf("routes: %+v", f.routes)
	}
	if len(f.records) != 1 || f.records[0]["type"] != "AAAA" || f.records[0]["proxied"] != true {
		t.Errorf("DNS records: %+v", f.records)
	}
}

func TestCheck(t *testing.T) {
	d, _ := newTest(t)
	if err := d.Check(context.Background()); err != nil {
		t.Fatal(err)
	}
	d.opts.AllowedSuffix = ".other.com"
	d.opts.Host = "auth.other.com"
	err := d.Check(context.Background())
	if err == nil || !strings.Contains(err.Error(), "AllowedSuffix") || !strings.Contains(err.Error(), "not in Zone") {
		t.Errorf("got %v", err)
	}
}

// The Worker itself, run in Node: back to the environment the cookie names,
// path and query unchanged; nowhere for a missing or odd cookie.
func TestRelayScript(t *testing.T) {
	if _, err := exec.LookPath("node"); err != nil {
		t.Skip("node not installed")
	}
	d, _ := newTest(t)
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "relay.mjs"), []byte(d.Script()), 0o644)
	os.WriteFile(filepath.Join(dir, "run.mjs"), []byte(`
import relay from "./relay.mjs";
const cases = [
  ["other=1; oauth_return=hz-r56z2ye4-api.example.com", "/api/auth/google/callback?code=abc&scope=email"],
  ["oauth_return=api-staging.example.com", "/auth/google/callback?code=abc"],
  ["", "/auth/google/callback?code=abc"],
  ["oauth_return=evil.com", "/auth/google/callback"],
  ["oauth_return=evilexample.com", "/auth/google/callback"],
  ["oauth_return=hz-auth.example.com", "/auth/google/callback"],
];
for (const [cookie, path] of cases) {
  const res = await relay.fetch(new Request("https://hz-auth.example.com" + path, { headers: { Cookie: cookie } }));
  console.log(res.status, res.headers.get("Location") || "-");
}
`), 0o644)
	out, err := exec.Command("node", filepath.Join(dir, "run.mjs")).CombinedOutput()
	if err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	want := "302 https://hz-r56z2ye4-api.example.com/api/auth/google/callback?code=abc&scope=email\n" +
		"302 https://api-staging.example.com/auth/google/callback?code=abc\n400 -\n400 -\n400 -\n400 -\n"
	if string(out) != want {
		t.Errorf("got:\n%swant:\n%s", out, want)
	}
}
