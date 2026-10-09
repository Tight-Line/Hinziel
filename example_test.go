package heinzel_test

import (
	"strings"

	"github.com/Tight-Line/heinzel"
	"github.com/Tight-Line/heinzel/agent/claudecode"
	"github.com/Tight-Line/heinzel/deploy"
	"github.com/Tight-Line/heinzel/deploy/cfworkers"
	"github.com/Tight-Line/heinzel/deploy/dockercompose"
	"github.com/Tight-Line/heinzel/dns/cloudflare"
	"github.com/Tight-Line/heinzel/env"
	"github.com/Tight-Line/heinzel/env/digitalocean"
	"github.com/Tight-Line/heinzel/forge/gitlab"
	"github.com/Tight-Line/heinzel/notify"
	"github.com/Tight-Line/heinzel/notify/slack"
	"github.com/Tight-Line/heinzel/notify/ticket"
	"github.com/Tight-Line/heinzel/tracker"
	"github.com/Tight-Line/heinzel/tracker/trello"
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
		APIKey: heinzel.Env("TRELLO_API_KEY"),
		Token:  heinzel.Env("TRELLO_TOKEN"),
		Board:  "FbGOL7Wb",
		Stages: map[tracker.Stage]trello.Stage{tracker.StageInProgress: {List: "In progress"}},
	})

	heinzel.Main(heinzel.Factory{
		Name:    "blanktracker",
		Tracker: board,
		Repos: []heinzel.Repo{{
			Name:         "app",
			TargetBranch: "develop",
			Forge:        gitlab.New(gitlab.Options{Project: "blanktracker/app", Token: heinzel.Env("GITLAB_TOKEN")}),
		}},
		Agent:   claudecode.New(claudecode.Options{Model: "claude-opus-5-5", MaxTurns: 200}),
		EnvName: func(t *tracker.Ticket) string { return "hz-" + strings.ToLower(t.Key) },

		Environment: digitalocean.New(digitalocean.Options{
			Token: heinzel.Env("DO_TOKEN"), Region: "fra1", Size: "s-2vcpu-4gb", Image: "docker-24-04",
			SSHKeys: []string{"ab:cd:…"}, SSHKeyPath: heinzel.Env("HEINZEL_SSH_KEY"),
		}),
		DNS: cloudflare.New(cloudflare.Options{
			Token: heinzel.Env("CLOUDFLARE_DNS_TOKEN"), Zone: zone, Proxied: true, AllowedNames: "hz-*",
		}),
		Endpoints: []heinzel.Endpoint{
			{Name: "api", URL: previewURL("-api"), DNS: heinzel.HostAddress(0)},
			{Name: "nova", URL: previewURL("-nova"), DNS: heinzel.HostAddress(0)},
			{Name: "web", URL: func(e *env.Environment) string { return cfworkers.PreviewURL(e.Name, previewWorker, workers) }},
		},
		EnvironmentActions: func(e *env.Environment) []env.Action {
			return []env.Action{{
				ID:    "google-oauth",
				Title: "Allow Google login on " + e.Name,
				Body: "Add these authorized redirect URIs to the **BlankTracker previews** OAuth client:\n\n" +
					"- " + e.Endpoints["api"] + "/auth/google/callback\n- " + e.Endpoints["api"] + "/api/auth/google/callback\n",
				Before: string(heinzel.StepPublish),
			}}
		},
		Deployers: []heinzel.Deployer{
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
					CertFile: heinzel.Env("ORIGIN_CERT_FILE"), KeyFile: heinzel.Env("ORIGIN_KEY_FILE"),
					Upstream: "nginx:80", Network: "blanktracker",
				},
				PostDeploy: []dockercompose.Exec{{Service: "app", Command: "php artisan migrate --force"}},
				URL:        func(e *env.Environment) string { return e.Endpoints["api"] },
			})},
			{Name: "web", Repo: "app", Deployer: cfworkers.New(cfworkers.Options{
				AccountID: heinzel.Env("CLOUDFLARE_ACCOUNT_ID"),
				Token:     heinzel.Env("CLOUDFLARE_WORKERS_TOKEN"),
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
		Notifiers: []heinzel.Notifier{
			{On: notify.Problems, Notifier: ticket.New(board)},
			{On: []notify.EventType{notify.EventRunActionRequired}, Notifier: slack.New(heinzel.Env("SLACK_OPS_WEBHOOK"))},
			{On: notify.Finished, Notifier: slack.New(heinzel.Env("SLACK_DEV_WEBHOOK"))},
		},
	})
}
