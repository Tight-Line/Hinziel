package skrat_test

import (
	"strings"

	"github.com/Tight-Line/skrat"
	"github.com/Tight-Line/skrat/agent/claudecode"
	"github.com/Tight-Line/skrat/deploy"
	"github.com/Tight-Line/skrat/deploy/cfworkers"
	"github.com/Tight-Line/skrat/deploy/dockercompose"
	"github.com/Tight-Line/skrat/dns/cloudflare"
	"github.com/Tight-Line/skrat/env"
	"github.com/Tight-Line/skrat/env/digitalocean"
	"github.com/Tight-Line/skrat/forge/gitlab"
	"github.com/Tight-Line/skrat/notify"
	"github.com/Tight-Line/skrat/notify/slack"
	"github.com/Tight-Line/skrat/notify/ticket"
	"github.com/Tight-Line/skrat/tracker"
	"github.com/Tight-Line/skrat/tracker/trello"
)

// The factory of README.md's Scenario A, built from the ready-made
// components. It's compiled, not run: it keeps the README honest.
func Example_blanktracker() {
	const (
		zone          = "blanktracker.com"
		previewWorker = "blanktracker-frontend-preview"
		workers       = "blanktracker" // the account's workers.dev subdomain
	)
	previewURL := func(suffix string) func(*env.Environment) string {
		return func(e *env.Environment) string { return "https://" + e.Name + suffix + "." + zone }
	}
	board := trello.New(trello.Options{
		APIKey: skrat.Env("TRELLO_API_KEY"),
		Token:  skrat.Env("TRELLO_TOKEN"),
		Board:  "FbGOL7Wb",
		Stages: map[tracker.Stage]trello.Stage{tracker.StageInProgress: {List: "In progress"}},
	})

	skrat.Main(skrat.Factory{
		Name:    "blanktracker",
		Tracker: board,
		Repos: []skrat.Repo{{
			Name:         "app",
			TargetBranch: "develop",
			Forge:        gitlab.New(gitlab.Options{Project: "blanktracker/app", Token: skrat.Env("GITLAB_TOKEN")}),
		}},
		Agent:   claudecode.New(claudecode.Options{Model: "claude-opus-5-5", MaxTurns: 200}),
		EnvName: func(t *tracker.Ticket) string { return "sk-" + strings.ToLower(t.Key) },

		Environment: digitalocean.New(digitalocean.Options{
			Token: skrat.Env("DO_TOKEN"), Region: "fra1", Size: "s-2vcpu-4gb", Image: "docker-24-04",
			SSHKeys: []string{"ab:cd:…"}, SSHKeyPath: skrat.Env("SKRAT_SSH_KEY"),
		}),
		DNS: cloudflare.New(cloudflare.Options{
			Token: skrat.Env("CLOUDFLARE_DNS_TOKEN"), Zone: zone, Proxied: true, AllowedNames: "sk-*",
		}),
		Endpoints: []skrat.Endpoint{
			{Name: "api", URL: previewURL("-api"), DNS: skrat.HostAddress(0)},
			{Name: "nova", URL: previewURL("-nova"), DNS: skrat.HostAddress(0)},
			{Name: "web", URL: func(e *env.Environment) string { return cfworkers.PreviewURL(e.Name, previewWorker, workers) }},
		},
		EnvironmentActions: func(e *env.Environment) []env.Action {
			return []env.Action{{
				ID:    "google-oauth",
				Title: "Allow Google login on " + e.Name,
				Body: "Add these authorized redirect URIs to the **BlankTracker previews** OAuth client:\n\n" +
					"- " + e.Endpoints["api"] + "/auth/google/callback\n- " + e.Endpoints["api"] + "/api/auth/google/callback\n",
				Before: string(skrat.StepPublish),
			}}
		},
		Deployers: []skrat.Deployer{
			{Name: "api", Repo: "app", Deployer: dockercompose.New(dockercompose.Options{
				Files:    []string{"vibe.docker-compose.yaml"},
				Override: "services:\n  cron:\n    profiles: [disabled]\n",
				EnvFile:  "ONSOCIAL_DISABLED=true\nSCRAPECREATORS_DISABLED=true\nMAIL_MAILER=log\n",
				Env: func(e *env.Environment) map[string]string {
					return map[string]string{
						"APP_URL":              e.Endpoints["api"],
						"FRONTEND_URL":         e.Endpoints["web"],
						"GOOGLE_REDIRECT_HOST": e.Endpoints["api"],
					}
				},
				TLS: &dockercompose.TLS{
					CertFile: skrat.Env("ORIGIN_CERT_FILE"), KeyFile: skrat.Env("ORIGIN_KEY_FILE"),
					Upstream: "nginx:80", Network: "blanktracker",
				},
				PostDeploy: []dockercompose.Exec{{Service: "app", Command: "php artisan migrate --force"}},
				URL:        func(e *env.Environment) string { return e.Endpoints["api"] },
			})},
			{Name: "web", Repo: "app", Deployer: cfworkers.New(cfworkers.Options{
				AccountID: skrat.Env("CLOUDFLARE_ACCOUNT_ID"),
				Token:     skrat.Env("CLOUDFLARE_WORKERS_TOKEN"),
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
		Notifiers: []skrat.Notifier{
			{On: notify.Problems, Notifier: ticket.New(board)},
			{On: []notify.EventType{notify.EventRunActionRequired}, Notifier: slack.New(skrat.Env("SLACK_OPS_WEBHOOK"))},
			{On: notify.Finished, Notifier: slack.New(skrat.Env("SLACK_DEV_WEBHOOK"))},
		},
	})
}
