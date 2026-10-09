// Package digitalocean is an env.Provisioner that gives every environment
// its own DigitalOcean droplet.
//
// Droplets are named after the environment and tagged "heinzel", plus one tag
// per label (e.g. "heinzel-factory:shop"). Ensure only ever adopts a droplet
// that carries the "heinzel" tag, so it never takes over, or later destroys,
// a droplet it didn't create.
package digitalocean

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/Tight-Line/heinzel/core"
	"github.com/Tight-Line/heinzel/core/remote"
	"github.com/Tight-Line/heinzel/env"
	"github.com/Tight-Line/heinzel/internal/apiclient"
)

// Tag marks every droplet this package creates.
const Tag = "heinzel"

// Options configure the droplets.
type Options struct {
	Token  string
	Region string // e.g. "fra1"
	Size   string // e.g. "s-2vcpu-4gb"
	Image  string // slug or ID, e.g. "docker-24-04" (Ubuntu with Docker)
	// SSHKeys are fingerprints or IDs of keys in the DigitalOcean account,
	// installed for SSHUser on every droplet.
	SSHKeys []string
	// SSHKeyPath is the private key Heinzel and the deployers use to reach
	// the droplets; "" = ssh's defaults and agent.
	SSHKeyPath string
	SSHUser    string // "" = "root"
	// UserData is a cloud-init script run on first boot. Ensure waits for
	// cloud-init to finish before it returns.
	UserData string
	Tags     []string // extra tags
	VPC      string   // VPC UUID; "" = the region's default
	// KnownHostsDir holds one known_hosts file per environment, pinning each
	// droplet's host key on first contact. "" = $XDG_STATE_HOME/heinzel/known_hosts.
	KnownHostsDir string
	// BaseURL of the API; "" = https://api.digitalocean.com.
	BaseURL string
}

// Provisioner creates droplets.
type Provisioner struct {
	opts Options
	api  *apiclient.Client

	// Tests replace these.
	poll time.Duration
	ssh  []string
}

var (
	_ env.Provisioner = (*Provisioner)(nil)
	_ env.Lister      = (*Provisioner)(nil)
	_ core.Checker    = (*Provisioner)(nil)
)

// New returns a DigitalOcean provisioner.
func New(opts Options) *Provisioner {
	base := strings.TrimRight(opts.BaseURL, "/")
	if base == "" {
		base = "https://api.digitalocean.com"
	}
	if opts.SSHUser == "" {
		opts.SSHUser = "root"
	}
	return &Provisioner{opts: opts, poll: 5 * time.Second, api: &apiclient.Client{
		Name:    "digitalocean",
		BaseURL: base + "/v2",
		Auth:    func(r *http.Request) { r.Header.Set("Authorization", "Bearer "+opts.Token) },
	}}
}

type droplet struct {
	ID       int      `json:"id"`
	Name     string   `json:"name"`
	Status   string   `json:"status"`
	Tags     []string `json:"tags"`
	Networks struct {
		V4 []struct {
			IPAddress string `json:"ip_address"`
			Type      string `json:"type"`
		} `json:"v4"`
	} `json:"networks"`
	Region struct {
		Slug string `json:"slug"`
	} `json:"region"`
	SizeSlug string `json:"size_slug"`
}

func (d *droplet) publicIP() string {
	for _, n := range d.Networks.V4 {
		if n.Type == "public" {
			return n.IPAddress
		}
	}
	return ""
}

// Ensure returns the environment's droplet, creating it if needed, once it's
// active, answers on SSH and has finished cloud-init.
func (p *Provisioner) Ensure(ctx context.Context, spec env.Spec) (*env.Environment, error) {
	d, err := p.find(ctx, spec.Name)
	if err != nil {
		return nil, err
	}
	if d == nil {
		if d, err = p.create(ctx, spec); err != nil {
			return nil, err
		}
		// A new droplet has a new host key, possibly on an old droplet's IP.
		os.Remove(p.knownHosts(spec.Name))
	}
	if d, err = p.waitActive(ctx, d.ID); err != nil {
		return nil, err
	}
	e := p.environment(d)
	if err := p.waitReady(ctx, e); err != nil {
		return nil, err
	}
	return e, nil
}

// find returns the droplet named name, or nil. A droplet of that name that
// Heinzel didn't create is an error, never adopted.
func (p *Provisioner) find(ctx context.Context, name string) (*droplet, error) {
	var resp struct {
		Droplets []droplet `json:"droplets"`
	}
	if err := p.api.Do(ctx, http.MethodGet, "/droplets", url.Values{"name": {name}}, nil, &resp); err != nil {
		return nil, err
	}
	for _, d := range resp.Droplets {
		if d.Name != name {
			continue
		}
		if !slices.Contains(d.Tags, Tag) {
			return nil, fmt.Errorf("droplet %s (%d) exists but wasn't created by Heinzel (no %q tag); refusing to use it", name, d.ID, Tag)
		}
		return &d, nil
	}
	return nil, nil
}

var tagUnsafe = regexp.MustCompile(`[^A-Za-z0-9_:-]`)

func (p *Provisioner) create(ctx context.Context, spec env.Spec) (*droplet, error) {
	tags := []string{Tag}
	keys := make([]string, 0, len(spec.Labels))
	for k := range spec.Labels {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	for _, k := range keys {
		tags = append(tags, tagUnsafe.ReplaceAllString(k+":"+spec.Labels[k], "_"))
	}
	tags = append(tags, p.opts.Tags...)
	body := map[string]any{
		"name":   spec.Name,
		"region": p.opts.Region,
		"size":   p.opts.Size,
		"image":  p.opts.Image,
		"tags":   tags,
	}
	if len(p.opts.SSHKeys) > 0 {
		body["ssh_keys"] = p.opts.SSHKeys
	}
	if p.opts.UserData != "" {
		body["user_data"] = p.opts.UserData
	}
	if p.opts.VPC != "" {
		body["vpc_uuid"] = p.opts.VPC
	}
	var resp struct {
		Droplet droplet `json:"droplet"`
	}
	if err := p.api.Do(ctx, http.MethodPost, "/droplets", nil, body, &resp); err != nil {
		return nil, fmt.Errorf("creating droplet %s: %w", spec.Name, err)
	}
	return &resp.Droplet, nil
}

func (p *Provisioner) get(ctx context.Context, id int) (*droplet, error) {
	var resp struct {
		Droplet droplet `json:"droplet"`
	}
	if err := p.api.Do(ctx, http.MethodGet, "/droplets/"+strconv.Itoa(id), nil, nil, &resp); err != nil {
		return nil, err
	}
	return &resp.Droplet, nil
}

func (p *Provisioner) waitActive(ctx context.Context, id int) (*droplet, error) {
	for {
		d, err := p.get(ctx, id)
		var retry *core.RetryableError
		if err != nil && !errors.As(err, &retry) {
			return nil, err
		}
		if err == nil && d.Status == "active" && d.publicIP() != "" {
			return d, nil
		}
		select {
		case <-ctx.Done():
			return nil, fmt.Errorf("waiting for droplet %d to become active: %w", id, ctx.Err())
		case <-time.After(p.poll):
		}
	}
}

func (p *Provisioner) environment(d *droplet) *env.Environment {
	return &env.Environment{
		Name:     d.Name,
		Provider: "digitalocean",
		ID:       strconv.Itoa(d.ID),
		Hosts: []env.Host{{
			Name:    "main",
			Address: d.publicIP(),
			SSH: &env.SSHAccess{
				User:           p.opts.SSHUser,
				Port:           22,
				KeyPath:        p.opts.SSHKeyPath,
				KnownHostsPath: p.knownHosts(d.Name),
			},
		}},
		State: map[string]string{"region": d.Region.Slug, "size": d.SizeSlug},
	}
}

// waitReady waits until SSH answers and cloud-init has finished.
func (p *Provisioner) waitReady(ctx context.Context, e *env.Environment) error {
	if err := os.MkdirAll(filepath.Dir(p.knownHosts(e.Name)), 0o700); err != nil {
		return err
	}
	h, err := remote.FromEnv(e.Hosts[0])
	if err != nil {
		return err
	}
	h.SSH = p.ssh
	if err := h.WaitReady(ctx, p.poll); err != nil {
		return err
	}
	const cloudInit = "if command -v cloud-init >/dev/null; then cloud-init status --wait >/dev/null 2>&1 || true; fi"
	return h.Run(ctx, cloudInit, nil, nil)
}

// Destroy deletes the environment's droplet. A missing droplet is not an error.
func (p *Provisioner) Destroy(ctx context.Context, e *env.Environment) error {
	id, _ := strconv.Atoi(e.ID)
	if id == 0 {
		d, err := p.find(ctx, e.Name)
		if err != nil || d == nil {
			return err
		}
		id = d.ID
	}
	err := p.api.Do(ctx, http.MethodDelete, "/droplets/"+strconv.Itoa(id), nil, nil, nil)
	if err != nil && !apiclient.IsStatus(err, http.StatusNotFound) {
		return err
	}
	os.Remove(p.knownHosts(e.Name))
	// Deletion is asynchronous; wait until the droplet is gone, so a List
	// right after doesn't still show it.
	for {
		_, err := p.get(ctx, id)
		if apiclient.IsStatus(err, http.StatusNotFound) {
			return nil
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf("waiting for droplet %d to be deleted: %w", id, ctx.Err())
		case <-time.After(p.poll):
		}
	}
}

// List returns every Heinzel droplet in the account.
func (p *Provisioner) List(ctx context.Context) ([]env.Environment, error) {
	var out []env.Environment
	for page := 1; ; page++ {
		var resp struct {
			Droplets []droplet `json:"droplets"`
		}
		q := url.Values{"tag_name": {Tag}, "per_page": {"200"}, "page": {strconv.Itoa(page)}}
		if err := p.api.Do(ctx, http.MethodGet, "/droplets", q, nil, &resp); err != nil {
			return nil, err
		}
		for i := range resp.Droplets {
			out = append(out, *p.environment(&resp.Droplets[i]))
		}
		if len(resp.Droplets) < 200 {
			return out, nil
		}
	}
}

// Check verifies the token, that every SSH key exists in the account, and
// that ssh is installed (Ensure uses it to wait for the droplet).
func (p *Provisioner) Check(ctx context.Context) error {
	ssh := "ssh"
	if len(p.ssh) > 0 {
		ssh = p.ssh[0]
	}
	toolErr := core.NeedTools(ssh)
	if err := p.api.Do(ctx, http.MethodGet, "/account", nil, nil, nil); err != nil {
		return errors.Join(toolErr, err)
	}
	errs := []error{toolErr}
	for _, k := range p.opts.SSHKeys {
		if err := p.api.Do(ctx, http.MethodGet, "/account/keys/"+url.PathEscape(k), nil, nil, nil); err != nil {
			errs = append(errs, fmt.Errorf("SSH key %s: %w", k, err))
		}
	}
	if p.opts.Region == "" || p.opts.Size == "" || p.opts.Image == "" {
		errs = append(errs, errors.New("Region, Size and Image are required"))
	}
	return errors.Join(errs...)
}

func (p *Provisioner) knownHosts(name string) string {
	dir := p.opts.KnownHostsDir
	if dir == "" {
		state := os.Getenv("XDG_STATE_HOME")
		if state == "" {
			home, _ := os.UserHomeDir()
			state = filepath.Join(home, ".local", "state")
		}
		dir = filepath.Join(state, "heinzel", "known_hosts")
	}
	return filepath.Join(dir, name)
}
