package digitalocean

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Tight-Line/heinzel/core/remote/remotetest"
	"github.com/Tight-Line/heinzel/env"
	"github.com/Tight-Line/heinzel/env/envtest"
)

// fakeDO serves droplets. A new droplet reports "new" on its first read and
// "active", with a public IP, after that.
type fakeDO struct {
	mu       sync.Mutex
	droplets map[int]map[string]any
	reads    map[int]int
	created  []map[string]any
	next     int
}

func newFakeDO() *fakeDO {
	return &fakeDO{droplets: map[int]map[string]any{}, reads: map[int]int{}, next: 100}
}

func (f *fakeDO) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if r.Header.Get("Authorization") != "Bearer do-token" {
		http.Error(w, `{"id":"unauthorized"}`, http.StatusUnauthorized)
		return
	}
	send := func(v any) { json.NewEncoder(w).Encode(v) }
	path := strings.TrimPrefix(r.URL.Path, "/v2")
	switch {
	case path == "/account":
		send(map[string]any{"account": map[string]string{"status": "active"}})
	case strings.HasPrefix(path, "/account/keys/"):
		if strings.TrimPrefix(path, "/account/keys/") != "ab:cd" {
			http.Error(w, `{"id":"not_found"}`, http.StatusNotFound)
			return
		}
		send(map[string]any{"ssh_key": map[string]string{}})
	case path == "/droplets" && r.Method == "GET":
		out := []map[string]any{}
		for _, d := range f.droplets {
			q := r.URL.Query()
			if (q.Get("name") == "" || d["name"] == q.Get("name")) &&
				(q.Get("tag_name") == "" || slices.Contains(d["tags"].([]string), q.Get("tag_name"))) {
				out = append(out, d)
			}
		}
		send(map[string]any{"droplets": out})
	case path == "/droplets" && r.Method == "POST":
		var body map[string]any
		json.NewDecoder(r.Body).Decode(&body)
		f.created = append(f.created, body)
		f.next++
		var tags []string
		for _, t := range body["tags"].([]any) {
			tags = append(tags, t.(string))
		}
		d := map[string]any{
			"id": f.next, "name": body["name"], "status": "new", "tags": tags,
			"networks": map[string]any{"v4": []any{}},
			"region":   map[string]string{"slug": body["region"].(string)}, "size_slug": body["size"],
		}
		f.droplets[f.next] = d
		w.WriteHeader(http.StatusAccepted)
		send(map[string]any{"droplet": d})
	case strings.HasPrefix(path, "/droplets/"):
		id, _ := strconv.Atoi(strings.TrimPrefix(path, "/droplets/"))
		d, ok := f.droplets[id]
		if !ok {
			http.Error(w, `{"id":"not_found"}`, http.StatusNotFound)
			return
		}
		if r.Method == "DELETE" {
			delete(f.droplets, id)
			w.WriteHeader(http.StatusNoContent)
			return
		}
		if f.reads[id]++; f.reads[id] > 1 {
			d["status"] = "active"
			d["networks"] = map[string]any{"v4": []map[string]string{
				{"ip_address": "10.0.0.5", "type": "private"},
				{"ip_address": "203.0.113.10", "type": "public"},
			}}
		}
		send(map[string]any{"droplet": d})
	default:
		http.NotFound(w, r)
	}
}

func newTestProvisioner(t *testing.T) (*Provisioner, *fakeDO) {
	f := newFakeDO()
	srv := httptest.NewServer(f)
	t.Cleanup(srv.Close)
	p := New(Options{
		Token: "do-token", Region: "fra1", Size: "s-2vcpu-4gb", Image: "docker-24-04",
		SSHKeys: []string{"ab:cd"}, SSHKeyPath: "/keys/heinzel", UserData: "#cloud-config\n",
		KnownHostsDir: t.TempDir(), BaseURL: srv.URL,
	})
	p.poll = time.Millisecond
	p.ssh = remotetest.LocalSSH(t)
	return p, f
}

func TestConformance(t *testing.T) {
	p, _ := newTestProvisioner(t)
	envtest.Run(t, p)
}

func TestEnsure(t *testing.T) {
	p, f := newTestProvisioner(t)
	ctx := context.Background()
	spec := env.Spec{Name: "hz-shop-t-1", Labels: map[string]string{"heinzel-factory": "shop", "heinzel-ticket": "T 1"}}

	// A stale known_hosts file from an earlier droplet of the same name.
	stale := p.knownHosts(spec.Name)
	os.WriteFile(stale, []byte("203.0.113.10 ssh-ed25519 OLD\n"), 0o600)

	e, err := p.Ensure(ctx, spec)
	if err != nil {
		t.Fatal(err)
	}
	h := e.Hosts[0]
	if e.ID != "101" || h.Address != "203.0.113.10" || h.SSH.User != "root" || h.SSH.KeyPath != "/keys/heinzel" {
		t.Errorf("environment %+v, host %+v", e, h.SSH)
	}
	if h.SSH.KnownHostsPath != filepath.Join(p.opts.KnownHostsDir, spec.Name) {
		t.Errorf("known hosts %s", h.SSH.KnownHostsPath)
	}
	if _, err := os.Stat(stale); !os.IsNotExist(err) {
		t.Error("a new droplet must not inherit the old host key")
	}
	body := f.created[0]
	tags, _ := json.Marshal(body["tags"])
	if string(tags) != `["heinzel","heinzel-factory:shop","heinzel-ticket:T_1"]` {
		t.Errorf("tags %s", tags)
	}
	if body["user_data"] != "#cloud-config\n" || body["image"] != "docker-24-04" {
		t.Errorf("request %+v", body)
	}
}

func TestForeignDropletIsNeverAdopted(t *testing.T) {
	p, f := newTestProvisioner(t)
	f.droplets[7] = map[string]any{"id": 7, "name": "hz-shop-t-1", "status": "active", "tags": []string{"prod"}}
	_, err := p.Ensure(context.Background(), env.Spec{Name: "hz-shop-t-1"})
	if err == nil || !strings.Contains(err.Error(), "wasn't created by Heinzel") {
		t.Fatalf("Ensure must refuse a droplet without the heinzel tag: %v", err)
	}
	if err := p.Destroy(context.Background(), &env.Environment{Name: "hz-shop-t-1"}); err == nil {
		t.Error("Destroy by name must refuse it too")
	}
	if _, ok := f.droplets[7]; !ok {
		t.Error("the foreign droplet was deleted")
	}
}

func TestCheck(t *testing.T) {
	p, _ := newTestProvisioner(t)
	if err := p.Check(context.Background()); err != nil {
		t.Fatal(err)
	}
	p.opts.SSHKeys = []string{"ab:cd", "ff:ff"}
	if err := p.Check(context.Background()); err == nil || !strings.Contains(err.Error(), "ff:ff") {
		t.Errorf("a missing SSH key must fail the check: %v", err)
	}
}

// TestIntegration creates and destroys a real droplet "hzct-<random>". It
// costs a few cents.
func TestIntegration(t *testing.T) {
	if os.Getenv("HEINZEL_IT_DIGITALOCEAN") == "" {
		t.Skip("set HEINZEL_IT_DIGITALOCEAN=1, DO_TOKEN, DO_SSH_KEY (fingerprint) and DO_SSH_KEY_PATH")
	}
	p := New(Options{
		Token: os.Getenv("DO_TOKEN"), Region: "fra1", Size: "s-1vcpu-1gb", Image: "ubuntu-24-04-x64",
		SSHKeys: []string{os.Getenv("DO_SSH_KEY")}, SSHKeyPath: os.Getenv("DO_SSH_KEY_PATH"), KnownHostsDir: t.TempDir(),
	})
	if err := p.Check(context.Background()); err != nil {
		t.Fatal(err)
	}
	envtest.Run(t, p)
}
