---
name: build-a-factory
description: Build, prove and hand over a Hinziel software factory for a team's app, so that a ticket becomes a branch, a preview environment and a merge request. Use when someone wants to install Hinziel on their project, set up a factory, get a preview environment per ticket, or "make the agent implement tickets". Also use to resume a factory installation that is under way (look for a hinziel/ directory or a factory repository with SETUP.md).
---

# Build a factory

You are installing Hinziel for a team. At the end they have a factory: a
small Go program that takes a ticket, has an agent implement it on a branch,
deploys the branch to an environment of its own and opens a merge request.
They also get the first written description of how their app is deployed.

The library is in https://github.com/Tight-Line/hinziel. Its README is the
reference for the concepts (§2), the interfaces (§3), how to write a factory
(§4) and the components that exist. This skill is the method; read the
README sections it points to when you need them, not up front.

## What makes this work

These come from installing Hinziel on a real app, where a guided setup,
one verified step at a time, was what made the installation work and what
the person enjoyed. Keep to them.

1. **One thing at a time.** Each turn asks the person for one thing: one
   decision, one credential, one click path. Say exactly where to click and
   what to choose, then wait for "done". A long list of everything up front
   is fine as an overview in Phase 1, never as the way to collect it.
2. **Verify each answer right away.** After every credential, call the
   service read-only and report what you found: whose token it is, its
   scopes, its expiry, and that it reaches the actual board, project, zone
   or account. Never print a secret. Once the factory builds, the check is
   `<factory> check <component>`.
3. **Derive what can be derived.** Never ask for a value you can look up:
   account IDs, subdomains, project numbers, fingerprints, existing OAuth
   clients. Non-secret values you find, you write into the env file
   yourself. Secrets the person types into the file; they never go into the
   chat.
4. **Evidence first, then a question with a recommendation.** Read the
   repos and the running environments before you ask. When the evidence
   doesn't settle something, ask, and say what you'd choose and why. Never
   fill a gap with a silent default.
5. **Follow the screen, not your memory of it.** Cloud consoles change. When
   the person sends a screenshot that doesn't match your instructions, map
   your instructions onto what's actually there.
6. **Say what a step touches.** Before anything that creates resources,
   costs money or is visible to the team (a droplet, DNS records, a comment
   on a ticket, an MR), say what it will create and get a yes. When the
   person asks "will this break anything?", answer concretely: what changes
   and what doesn't.
7. **Skipping is always allowed.** When the person wants to skip or defer a
   part (a notifier, a trigger, a dedicated OAuth client), say in one line
   what it costs, adjust the factory, and move on.
8. **Write it down where the team can review it.** Progress, decisions and
   the environment model go into files in the factory repository
   (`SETUP.md`, `ENVIRONMENT.md`), not only into this conversation. A new
   session must be able to pick up from those files alone.
9. **Fix Hinziel, not just the factory.** When a run fails because of
   Hinziel itself (a component's check that should have caught it, a
   missing feature), say so, and fix it upstream or record it as an issue.
   The factory should never carry a workaround silently.

## Where things go

- **The factory:** in a dedicated factory repository by default; in a
  directory of the app's repository (`hinziel/`) when the app is a monorepo
  (README §4). Ask which, with that recommendation.
- **Secrets:** `~/.config/hinziel/<factory>.env` on the machine that runs
  the factory, mode 600, with a commented placeholder for every value: what
  it is, where to create it and the least access that works. Load it into a
  command's environment with `set -a; . <file>; set +a`. Never commit it.
- **`SETUP.md`** in the factory repository: the installation's progress, as
  a checklist with the decisions and why. Update it as you go.
- **`ENVIRONMENT.md`** in the factory repository: the environment model
  (`templates/ENVIRONMENT.md`).
- **`README.md`** in the factory directory: the handover
  (`templates/FACTORY-README.md`).
- **Commits:** ask how the team works before the first commit. Some want
  their own branch and to commit and push themselves; some want you to
  commit on a branch they make. Never push without being asked.

## Phase 1: plan (no code yet)

Goal: the person confirms a plan and knows everything they'll be asked for.

1. **Repositories.** One or several? Monorepo or not? With several, which
   repos make up the system, and is there one that knows how they run
   together (infra, deploy, platform)? Start there.
2. **The team's process.** Read `CLAUDE.md`, contributing guides and recent
   history. Where do tickets live? What is the target branch? How do
   changes get reviewed? What checks must pass before a change is done
   (`./toolkit check`, `make lint test`)? Note every rule written for a
   person at a keyboard ("push to `vibe`", "ask before every git
   operation"): Hinziel's agent prompt overrides git, MR and confirmation
   rules, but the rest still applies.
3. **How the app is deployed.** This is the hard part and most of the
   value. Follow `references/environment.md`: dig through the evidence
   layer by layer, compare environments, and write `ENVIRONMENT.md`. Go
   through every category in `references/friction.md` and record what you
   found for each, including "not applicable".
4. **What the team's agents can reach.** Ask what their agents use today:
   a read-only replica of production data, internal APIs, MCP servers,
   skills in the repository and the access those assume. A developer's
   agent finds these through the developer's own SSH config and
   credentials. The factory's agent gets only what the factory gives it,
   so decide each one on purpose: wire it in, or leave it out.
5. **Choose components** from the README's package map. Name any that must
   be written, and say why none of the existing ones fits.
6. **Does the agent need a sandbox, and what goes in it.** Each level asks
   more of the host (README, "The agent's sandbox"): no sandbox needs
   nothing, `&Sandbox{}` needs Docker, `&Sandbox{Docker: true}` needs
   Sysbox too. Choose the least that's safe. Find what the team's "before
   you finish" checks need. An agent that only edits, pushes and tests
   against its environment needs no sandbox when its host holds the
   factory alone and one run at a time (a pod in the team's cluster); one
   that starts anything, or shares its host with other runs or other work,
   needs one, so what it started dies with it. Decide this together with
   step 7: where the factory runs limits which levels are possible. Hinziel's base image has git, ssh, Node
   22 and Claude Code. A dev stack in Docker Compose means
   `&Sandbox{Docker: true}`; other tools (PHP, Python, a database client)
   go in `Sandbox.Dockerfile` lines. Run the checks once yourself in a
   fresh checkout, timed: the first run in a sandbox builds the dev images
   from nothing, and BlankTracker's took 15 minutes. If it's long, say it
   in the factory's `Instructions` (run the check in the background: a
   single command of the agent is stopped after 10 minutes), and suggest
   the team publish its dev images to a registry so a sandbox pulls them
   instead of building them. That's the team's change to make, not the
   factory's.
7. **Where the factory runs: start from how the team runs things today.**
   Look first: Kubernetes manifests or Helm charts, Terraform, ECS task
   definitions, a compose file on a server, CI runners. Then ask how they
   run internal tools and what they'd rather not add. Fit the factory to
   the platform they have, not the other way round (README, "Hosting a
   factory"):
   - **A container platform they already run** (a dev Kubernetes cluster,
     ECS, Nomad): one long-lived container running `daemon`, exactly one
     replica so a trigger isn't answered twice, its state directory
     (`HINZIEL_STATE_DIR`) on a persistent volume, its variables from the
     platform's secrets. No sandbox is the natural fit: it needs no Docker
     socket and no privileges, which a cluster rarely gives. Keep
     `--runs 1` unless the agent starts nothing, since parallel agents
     share the container. Hinziel builds no image or manifest for this yet:
     write them in the factory repository, starting from
     `internal/hostinstall/factory.Dockerfile` plus the static factory
     binary, and record the gap. A pod per run isn't possible yet.
   - **A server or VM**: an always-on Linux host, prepared and updated by
     `host install`; it installs Docker, and Sysbox if needed, itself.
     Any sandbox level works there.
   - **A laptop**: fine for trying the factory with no sandbox or one
     without Docker inside, and for polling triggers; comments made while
     it's off are missed.
   A sandbox with Docker inside needs Sysbox, so Linux, never
   `--privileged`: Docker Desktop on a Mac can't run it, and most managed
   clusters can't either. Ask how many runs may execute at once
   (`--runs`): each holds its own agent, sandbox and dev stack, so size
   the host for that many.
8. **Triggers and notifications.** How will people start a run? The CLI
   always works. A comment trigger (`trigger/trellocomments`: a comment
   that @mentions the factory's member and says "implement") polls, so it
   needs no public URL and runs anywhere, a laptop included. Webhooks
   need a public HTTPS address and an always-on host. Who should
   hear about what? Problems and human actions can go on the ticket alone.
9. **List what's needed from people**: every credential, one-time setup and
   human action, each with what it is, why, the least access, and where it
   goes. Sources: each component's Credentials section, the app's
   `.env.example` and deploy scripts, and the environment model's human
   actions.
10. **Confirm the plan** in one message: components, the sandbox and where
   the factory runs, the environment model
   and how a preview differs from staging, how runs start, who's notified,
   what you'll need from them, what runs will cost (a droplet per ticket,
   say), and what you'll create where. Then ask: shall I build?

Write `SETUP.md` and `ENVIRONMENT.md` before asking, so the person can
review them as files.

## Phase 2: build and prove

1. **Write the factory** against the interfaces (README §4), and any
   missing components, each passing its conformance suite. Read every
   `hinziel.Env` while building the `Factory`, never inside a func the
   factory holds. Keep the app's own files (compose files, build scripts)
   unchanged; put what previews need differently in the factory (an
   override file, an env template). Take post-deploy commands from the
   team's own deploy scripts, in their order.
2. **Write the env file** with a placeholder and a comment for every value.
3. **Collect credentials one at a time** with the loop in
   `references/credentials.md`. Verify each before the next. Then run the
   full `check`.
4. **Bring up an environment** for the target branch alone (`env up`, or
   the first run's `deploy_base`). Check parity against the environment it
   copies (`references/environment.md`), not just that it answers.
5. **Prove the whole flow** with a real ticket: `references/first-run.md`.
   Watch it, report each step to the person as it finishes, and fix what
   fails. Expect the first run to find problems; each is either the
   factory's or Hinziel's.
6. **Install it where it runs.** On the team's container platform, apply
   the image and manifest from Phase 1 step 7 with their usual tooling,
   after showing them what it creates. On a server, use `<factory> host
   install --env-file <file> --binary <linux build> <host>` (after the
   first run, or for it, if the sandbox needs Docker and this machine
   can't run Sysbox). Say
   first what it installs (Docker, Sysbox if the sandbox has Docker
   inside, a `hinziel` user, a container) and that installing Sysbox
   restarts Docker. Put the command in a small
   script in the factory repository (BlankTracker: `hinziel/runner/deploy.sh`)
   so an update is one command. Before updating a host with a running
   daemon, make sure no run is executing; `host install` refuses otherwise.
7. **Hand over**: write the factory README from
   `templates/FACTORY-README.md`, finish `SETUP.md`, and tell the person
   what's still running and costing money.

## Resuming

If a factory directory or repository already exists, read `SETUP.md`
first and continue from the first unchecked item. Check that what it says
still holds: run `check`, look at the env file (never print its secrets),
and see whether the factory's source is all there. A build artifact
without its source means the source must be rewritten; a binary's strings
(embedded files, string literals) can recover much of it.

## References

- `references/environment.md`: learning how the app is deployed, the
  environment model, parity.
- `references/friction.md`: every category of friction met so far, with the
  evidence to look for and the question to ask.
- `references/credentials.md`: the one-at-a-time loop, and notes per service.
- `references/first-run.md`: choosing a test ticket, watching the run,
  verifying, cleaning up.
- `templates/`: `SETUP.md`, `ENVIRONMENT.md` and the factory README.
