// Command blanktracker is the factory of README.md's Scenario A, built from
// the ready-made components. It keeps the README honest: it's compiled with
// the rest of the module, so a change that breaks the README breaks the build.
package main

import (
	"strings"

	"github.com/Tight-Line/hinziel"
	"github.com/Tight-Line/hinziel/agent/claudecode"
	"github.com/Tight-Line/hinziel/deploy"
	"github.com/Tight-Line/hinziel/deploy/cfworkers"
	"github.com/Tight-Line/hinziel/deploy/dockercompose"
	"github.com/Tight-Line/hinziel/dns/cloudflare"
	"github.com/Tight-Line/hinziel/env"
	"github.com/Tight-Line/hinziel/env/digitalocean"
	"github.com/Tight-Line/hinziel/forge/gitlab"
	"github.com/Tight-Line/hinziel/notify"
	"github.com/Tight-Line/hinziel/notify/slack"
	"github.com/Tight-Line/hinziel/notify/ticket"
	"github.com/Tight-Line/hinziel/tracker"
	"github.com/Tight-Line/hinziel/tracker/trello"
	"github.com/Tight-Line/hinziel/trigger/trellocomments"
)

func main() {
	const (
		zone          = "blanktracker.com"
		previewWorker = "blanktracker-frontend-preview"
		workers       = "blanktracker" // the account's workers.dev subdomain
		oauthRelay    = "https://auth-relay." + zone
	)
	previewURL := func(suffix string) func(*env.Environment) string {
		return func(e *env.Environment) string { return "https://" + e.Name + suffix + "." + zone }
	}
	board := trello.New(trello.Options{
		APIKey: hinziel.Env("TRELLO_API_KEY"),
		Token:  hinziel.Env("TRELLO_TOKEN"),
		Board:  "FbGOL7Wb",
		Stages: map[tracker.Stage]trello.Stage{tracker.StageInProgress: {List: "In progress"}},
	})

	hinziel.Main(hinziel.Factory{
		Name:    "blanktracker",
		Tracker: board,
		Repos: []hinziel.Repo{{
			Name:         "app",
			TargetBranch: "develop",
			Forge:        gitlab.New(gitlab.Options{Project: "blanktracker/app", Token: hinziel.Env("GITLAB_TOKEN")}),
		}},
		Agent:   claudecode.New(claudecode.Options{Model: "claude-opus-5-5", MaxTurns: 200}),
		EnvName: func(t *tracker.Ticket) string { return "hz-" + strings.ToLower(t.Key) },

		Environment: digitalocean.New(digitalocean.Options{
			Token: hinziel.Env("DO_TOKEN"), Region: "fra1", Size: "s-2vcpu-4gb", Image: "docker-20-04",
			SSHKeys: []string{"ab:cd:…"}, SSHKeyPath: hinziel.Env("HINZIEL_SSH_KEY"),
		}),
		DNS: cloudflare.New(cloudflare.Options{
			Token: hinziel.Env("CLOUDFLARE_DNS_TOKEN"), Zone: zone, Proxied: true, AllowedNames: "hz-*",
		}),
		Endpoints: []hinziel.Endpoint{
			{Name: "api", URL: previewURL("-api"), DNS: hinziel.HostAddress(0)},
			{Name: "nova", URL: previewURL("-nova"), DNS: hinziel.HostAddress(0)},
			{Name: "web", URL: func(e *env.Environment) string { return cfworkers.PreviewURL(e.Name, previewWorker, workers) }},
		},
		Deployers: []hinziel.Deployer{
			{Name: "api", Repo: "app", Deployer: dockercompose.New(dockercompose.Options{
				Files:    []string{"vibe.docker-compose.yaml"},
				Override: "services:\n  cron:\n    profiles: [disabled]\n",
				EnvFile:  "ONSOCIAL_DISABLED=true\nSCRAPECREATORS_DISABLED=true\nMAIL_MAILER=log\n",
				Env: func(e *env.Environment) map[string]string {
					return map[string]string{
						"APP_URL":              e.Endpoints["api"],
						"FRONTEND_URL":         e.Endpoints["web"],
						"GOOGLE_REDIRECT_HOST": oauthRelay,
					}
				},
				TLS: &dockercompose.TLS{
					CertFile: hinziel.Env("ORIGIN_CERT_FILE"), KeyFile: hinziel.Env("ORIGIN_KEY_FILE"),
					Upstream: "nginx:80", Network: "blanktracker",
					ReturnCookie: &dockercompose.ReturnCookie{Domain: "." + zone, Paths: []string{"/auth/google", "/api/auth/google"}},
				},
				PostDeploy: []dockercompose.Exec{{Service: "app", Command: "php artisan migrate --force"}},
				URL:        func(e *env.Environment) string { return e.Endpoints["api"] },
			})},
			{Name: "web", Repo: "app", Deployer: cfworkers.New(cfworkers.Options{
				AccountID: hinziel.Env("CLOUDFLARE_ACCOUNT_ID"),
				Token:     hinziel.Env("CLOUDFLARE_WORKERS_TOKEN"),
				Worker:    previewWorker,
				Build:     "npm ci && ./cloudflare-build.sh",
				BuildEnv: func(e *env.Environment, src *deploy.Source) map[string]string {
					return map[string]string{
						"API_URL":                e.Endpoints["api"] + "/api",
						"NOVA_URL":               e.Endpoints["nova"] + "/nova",
						"WORKERS_CI_COMMIT_SHA":  src.Commit,
						"VITE_EMAIL_TRACKER_URL": "https://email-tracker.invalid",
					}
				},
			})},
		},
		Triggers: []hinziel.Trigger{{Name: "trello", Trigger: trellocomments.New(trellocomments.Options{
			APIKey: hinziel.Env("TRELLO_API_KEY"), Token: hinziel.Env("TRELLO_TOKEN"), Board: "FbGOL7Wb",
		})}},
		Notifiers: []hinziel.Notifier{
			{On: notify.Problems, Notifier: ticket.New(board)},
			{On: notify.Finished, Notifier: slack.New(hinziel.Env("SLACK_DEV_WEBHOOK"))},
		},
	})
}
