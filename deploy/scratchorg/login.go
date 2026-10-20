package scratchorg

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/Tight-Line/hinziel/core"
)

// Logging the Dev Hub in through a human. sf's own web login can't work on a
// factory host: it sends the browser back to localhost:1717, the machine
// sf runs on, which isn't where the human's browser is. So Ensure builds the
// same login link sf would (sf's built-in connected app, with PKCE), asks a
// human to log in and send back the address the browser ended on (a page
// that doesn't load), and finishes the login from the code in it. The code
// is single-use and worthless without the PKCE verifier, which never leaves
// this host, so it can be posted on a ticket.

const (
	sfClientID    = "PlatformCLI" // sf's built-in connected app
	sfRedirectURI = "http://localhost:1717/OauthRedirect"
	sfScopes      = "refresh_token api web"
)

// loginQuestionID names the answer to the login request.
func (o *Org) loginQuestionID() string { return "scratchorg.login." + o.opts.DevHub }

func (o *Org) loginURL() string {
	if o.opts.LoginURL != "" {
		return strings.TrimRight(o.opts.LoginURL, "/")
	}
	return "https://login.salesforce.com"
}

// sfErrorNames are sf's errors for a Dev Hub that isn't logged in, or whose
// login no longer works.
var sfErrorNames = []string{"NamedOrgNotFoundError", "NoAuthInfoFound", "RefreshTokenAuthError", "AuthInfoCreationError"}

// needsLogin reports whether err means the Dev Hub needs logging in.
func needsLogin(err error) bool {
	var se *sfError
	if !errors.As(err, &se) {
		return false
	}
	for _, n := range sfErrorNames {
		if se.Name == n {
			return true
		}
	}
	m := strings.ToLower(se.Message)
	return strings.Contains(m, "invalid_grant") || strings.Contains(m, "expired access/refresh token")
}

// pendingLogin is what a login request keeps until it's answered.
type pendingLogin struct {
	Verifier string `json:"verifier"`
	State    string `json:"state"`
}

// loginFile is where the pending login waits: under the user's config
// directory, readable by them only.
func (o *Org) loginFile() (string, error) {
	dir, err := os.UserConfigDir()
	if err != nil {
		return "", err
	}
	name := regexp.MustCompile(`[^A-Za-z0-9._-]+`).ReplaceAllString(o.opts.DevHub, "_")
	return filepath.Join(dir, "hinziel", "scratchorg", name+"-login.json"), nil
}

// devHubLogin is called when the Dev Hub needs logging in. With an answer to
// an earlier request, it finishes that login; otherwise, or if that fails,
// it asks a human for one (core.NeedsInputError).
func (o *Org) devHubLogin(ctx context.Context, cause error) error {
	why := fmt.Sprintf("Dev Hub `%s` isn't logged in on the factory's host, or its login has expired", o.opts.DevHub)
	if answer, ok := core.Answer(ctx, o.loginQuestionID()); ok {
		err := o.finishLogin(ctx, answer)
		if err == nil {
			return nil
		}
		why = fmt.Sprintf("That didn't log Dev Hub `%s` in: %v", o.opts.DevHub, err)
	}
	link, err := o.startLogin()
	if err != nil {
		return fmt.Errorf("%w; and asking for a login failed: %v", cause, err)
	}
	return &core.NeedsInputError{ID: o.loginQuestionID(), Question: fmt.Sprintf(`**%s.** I need it to create this ticket's scratch org.

1. Open [this login link](%s) and log in as a user of the Dev Hub's org with Dev Hub access.
2. Your browser then ends on a page at `+"`%s?code=…`"+` that doesn't load. That's expected.
3. Copy that page's whole address and send it back as the answer, within a few minutes: the code in it expires.

The address holds a one-time code that only works together with a secret kept on the factory's host, so it's safe to post.`,
		why, link, sfRedirectURI)}
}

// startLogin saves a new PKCE verifier and state, and returns the login link.
func (o *Org) startLogin() (string, error) {
	buf := make([]byte, 96)
	rand.Read(buf)
	st := make([]byte, 8)
	rand.Read(st)
	p := pendingLogin{Verifier: base64.RawURLEncoding.EncodeToString(buf), State: hex.EncodeToString(st)}
	path, err := o.loginFile()
	if err != nil {
		return "", err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return "", err
	}
	data, _ := json.Marshal(p)
	if err := os.WriteFile(path, data, 0o600); err != nil {
		return "", err
	}
	challenge := sha256.Sum256([]byte(p.Verifier))
	q := url.Values{
		"response_type":  {"code"},
		"client_id":      {sfClientID},
		"redirect_uri":   {sfRedirectURI},
		"state":          {p.State},
		"prompt":         {"login"},
		"scope":          {sfScopes},
		"code_challenge": {base64.RawURLEncoding.EncodeToString(challenge[:])},
	}
	return o.loginURL() + "/services/oauth2/authorize?" + q.Encode(), nil
}

// redirectRE finds the address the browser ended on, anywhere in an answer.
var redirectRE = regexp.MustCompile(`https?://localhost:1717/OauthRedirect\?[^\s<>()\x60"']+`)

// finishLogin exchanges the code in answer for a refresh token and logs sf in
// with it, as DevHub.
func (o *Org) finishLogin(ctx context.Context, answer string) error {
	m := redirectRE.FindString(answer)
	if m == "" {
		return fmt.Errorf("the answer has no address starting with %s", sfRedirectURI)
	}
	u, err := url.Parse(m)
	if err != nil {
		return err
	}
	q := u.Query()
	path, err := o.loginFile()
	if err != nil {
		return err
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return errors.New("no login is waiting for that address; use the newest link")
	}
	var p pendingLogin
	if err := json.Unmarshal(data, &p); err != nil {
		return err
	}
	switch {
	case q.Get("state") != p.State:
		return errors.New("that address comes from an older login link; use the newest one")
	case q.Get("error") != "":
		return fmt.Errorf("Salesforce refused the login: %s", strings.TrimSpace(q.Get("error")+" "+q.Get("error_description")))
	case q.Get("code") == "":
		return errors.New("the address has no code")
	}

	form := url.Values{
		"grant_type":    {"authorization_code"},
		"code":          {q.Get("code")},
		"client_id":     {sfClientID},
		"redirect_uri":  {sfRedirectURI},
		"code_verifier": {p.Verifier},
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, o.loginURL()+"/services/oauth2/token", strings.NewReader(form.Encode()))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := tokenClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	var tok struct {
		RefreshToken string `json:"refresh_token"`
		InstanceURL  string `json:"instance_url"`
		Error        string `json:"error"`
		Description  string `json:"error_description"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&tok); err != nil {
		return fmt.Errorf("Salesforce's token endpoint: %s", resp.Status)
	}
	if resp.StatusCode != http.StatusOK || tok.RefreshToken == "" {
		return fmt.Errorf("Salesforce refused the code (it may have expired): %s %s", tok.Error, tok.Description)
	}
	instance, err := url.Parse(tok.InstanceURL)
	if err != nil || instance.Host == "" {
		return fmt.Errorf("Salesforce returned no instance URL")
	}

	// sf takes the refresh token as an SFDX auth URL, from a file only this
	// user can read, deleted right after.
	f, err := os.CreateTemp("", "hinziel-sfdx-url-*")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	_, err = fmt.Fprintf(f, "force://%s::%s@%s", sfClientID, tok.RefreshToken, instance.Host)
	if err := errors.Join(err, f.Close()); err != nil {
		return err
	}
	args := []string{"org", "login", "sfdx-url", "--sfdx-url-file", f.Name()}
	if !strings.Contains(o.opts.DevHub, "@") { // an alias, not a username
		args = append(args, "--alias", o.opts.DevHub)
	}
	if err := o.sf(ctx, "", nil, args...); err != nil {
		return err
	}
	os.Remove(path)
	return nil
}

var tokenClient = &http.Client{Timeout: time.Minute}
