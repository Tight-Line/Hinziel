// Package hinziel is a library for building software factories: programs that
// take a ticket and, without human help, implement it on a branch, deploy it
// to a fresh environment, open merge requests and tell people about it.
//
// A factory is an ordinary Go program. It picks components (trackers, forges,
// agents, environments, deployers, notifiers) from this module's subpackages
// or from its own code, and hands them to Main, which provides the CLI:
//
//	func main() {
//		hinziel.Main(hinziel.Factory{
//			Name:    "blanktracker",
//			Tracker: trello.New(trello.Options{Board: "FbGOL7Wb", Token: hinziel.Env("TRELLO_TOKEN")}),
//			Repos: []hinziel.Repo{{
//				Name: "app", TargetBranch: "develop",
//				Forge: gitlab.New(gitlab.Options{Project: "blanktracker/app", Token: hinziel.Env("GITLAB_TOKEN")}),
//			}},
//			Agent:       claudecode.New(claudecode.Options{Model: "claude-opus-5-5"}),
//			Environment: digitalocean.New(digitalocean.Options{Region: "fra1"}),
//		})
//	}
//
// Components implement the interfaces in packages tracker, forge, agent, env,
// dns, deploy, notify and trigger. See README.md for the full design.
package hinziel
