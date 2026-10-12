# Friction to anticipate

Every installation meets the same kinds of friction. BlankTracker met mild
versions of all of these; real teams will meet harder ones. Go through each
category in Phase 1 and record what you found in `ENVIRONMENT.md` or
`SETUP.md`, including "not applicable".

For each: what to look for, what to ask when the evidence doesn't settle it,
and what BlankTracker showed.

## The team's own rules for agents and changes

- **Look for:** `CLAUDE.md`, `AGENTS.md`, `.claude/` skills, contributing
  guides, required spec documents, commit conventions, protected branches.
- **Ask:** which rules the factory's agent must follow, and which were
  written for a person at a keyboard. Who makes branches, commits and
  pushes the factory itself?
- **BlankTracker:** `CLAUDE.md` said "push to `vibe`" and "ask before every
  git operation", written for non-developers. Hinziel's agent prompt
  overrides git, MR and confirmation rules; the rest (a spec per change,
  `./toolkit check` before finishing) still applied, and the agent followed
  it. The owner made the branch and wanted to commit and push the factory
  themselves at first.

## Deploy knowledge spread around

- **Look for:** deploy scripts, CI jobs, Makefiles, runbooks, shell history
  on hosts, README sections.
- **Ask:** what happens on a deploy that isn't in any script.
- **BlankTracker:** post-deploy commands lived in `deploy-vibe.sh` only; the
  factory now runs them in the script's order.

## Where preview data comes from

- **Look for:** seeders, fixtures, dump scripts, what the dump excludes.
- **Ask:** what data testers need on a preview to test a typical ticket,
  and whether the existing dump has it.
- **BlankTracker:** the vibe dump came from production with sensitive and
  large tables excluded: reference data only, no contents. A ticket about
  mapping existing content values could only be tested on empty tables.
  The hard version: no dump at all, or only production.

## Side effects a preview must not cause

- **Look for:** schedulers (cron, Horizon, Celery beat), outbound mail,
  paid APIs, webhooks into shared systems, analytics, payment providers.
- **Ask:** which integrations must stay off on previews, and whether the
  app has switches for them.
- **BlankTracker:** a nightly schedule called paid APIs; the app had kill
  switches (`ONSOCIAL_DISABLED`, `SCRAPECREATORS_DISABLED`), mail went to
  the log, and the factory's override dropped the `cron` service.

## Resources shared between environments

- **Look for:** one database, queue, bucket or search index used by more
  than one environment; connection strings in templates.
- **Ask:** what a preview may read and write.
- **BlankTracker:** previews run their own Postgres and Redis in the stack.

## Secrets in the wrong places

- **Look for:** real keys in `.env.example`, committed `.env` files, CI
  variables shared with production.
- **Ask:** whether a committed secret is meant to be there and which
  environment it belongs to. Don't assume the worst; ask.
- **BlankTracker:** `.env.example` held a real Google OAuth client and
  secret. Its redirect URIs included production's, which looked alarming,
  but production uses a different Google project; the old URIs were left
  over. Previews reuse that dev client.

## The app assumes fixed hostnames

- **Look for:** CORS origins, cookie and session domains, Sanctum stateful
  domains, OAuth redirect URIs, absolute URLs in config, SPA build
  variables (`API_URL`).
- **Ask:** whether auth is cookie or token based, and what must change per
  hostname.
- **BlankTracker:** the SPA authenticates with a bearer token and the API
  allows any origin, so the SPA could stay on `workers.dev` while the API
  got `hz-<ticket>-api` in the production zone. Production sets no
  `SESSION_DOMAIN`, so its cookies never reach a preview.

## Setup only a person can do

- **Look for:** OAuth clients (no API to add redirect URIs), app-store
  consoles, licence portals, allowlists at a partner.
- **Ask:** who can do it, and how quickly. It becomes a human action per
  environment.
- **BlankTracker:** two Google redirect URIs per preview. The person added
  them while the agent worked, and asked for a way to never do it again.
  The answer was an OAuth relay: one callback host registered once,
  forwarding to the host a cookie (`oauth_return`) says the login started
  on. The owner chose to put it in the app (a middleware sets the cookie,
  the relay Worker lives in the repository), so any environment uses it,
  not only previews. Without app changes, Hinziel does both halves:
  `dockercompose.TLS.ReturnCookie` and `deploy/cfoauthrelay`.
- **Ask whether a fix should outlive Hinziel.** Friction a preview hits is
  often friction any new environment hits. Offer to solve it in the app or
  its infrastructure, where every environment benefits, and keep the
  factory-only workaround for teams that can't change the app.
- **Prefer removing the action to announcing it.** For OAuth login, check
  whether the app's flow is stateless or keeps its state somewhere that
  survives one more redirect; if so, use the relay. Only fall back to a
  human action per environment when nothing can automate it, and then
  `approve` confirms it at any time, so doing it early never holds the
  run up.

## The agent's checks need tooling the preview lacks

- **Look for:** what the team's "before you finish" command needs: a local
  dev stack, Docker, Node in a container, a database.
- **Ask:** how long the checks take from a fresh checkout, and whether the
  team publishes its dev images anywhere.
- **BlankTracker:** `./toolkit check` needs the local dev stack. On the
  developer's laptop, the agent ran `./toolkit init` in its own checkout
  and got a stack next to the developer's. On the hosted runner, the stack
  it left behind once published Postgres, Redis and nginx to the internet,
  because Docker's ports bypass ufw. The sandbox fixed both:
  `Sandbox{Docker: true}` gives each run a Docker daemon of its own
  (Sysbox), and the stack dies with the sandbox. What's left is time: the
  first check in an empty sandbox built the dev image for 15 minutes,
  longer than one command of the agent may take, so the factory's
  instructions tell it to run the check in the background. The lasting fix
  is the team's: publish the dev image to a registry so a sandbox pulls it.

## The agent inherits the host's access

- **Look for:** SSH config entries, cloud CLIs logged in, MCP servers,
  repo skills that reach production replicas or internal APIs.
- **Ask:** which of these the team means its agents to use, and which the
  factory's agent should get on purpose (see SKILL.md, Phase 1 step 4).
- **BlankTracker:** the agent answered a ticket's question about real data
  from the production read-only replica, through the repo's
  `manager-reports` skill over the developer's SSH access. The team keeps
  that replica for agents on purpose. On a hosted factory none of it would
  be there unless the factory sets it up.
- **The judgement call:** give the agent the narrowest credential that
  does the job, directly. For BlankTracker that meant the replica's own
  read-only database URL in `AgentEnv`, not an SSH key to the server that
  holds the database's keys, even though the repo's skill and `CLAUDE.md`
  go through that server. A team's own route was built for people on
  laptops; the factory's agent can often skip a hop. Decide case by case,
  and say when you deviate from the team's documented route and why.
  Check the network too: a replica that only trusts known IPs must trust
  the factory's host.
- **Since then:** the agent no longer inherits the factory's environment.
  It gets a minimal base, its own credentials and the factory's
  `AgentEnv`, in a sandbox that holds only its run's checkouts. Decide with
  the team what goes in `AgentEnv` (a read-only database URL, say). The
  factory's own secrets stay outside the sandbox: on a host, `host install`
  puts them in `/etc/hinziel/<factory>`, readable by root and the factory
  alone.

## Plan, permission and policy limits

- **Look for:** free tiers (GitLab: no project access tokens, 5 members in
  a private group), droplet limits, who administers which workspace.
- **Ask:** what the person can create themselves and what needs an admin.
- **BlankTracker:** GitLab free tier, so a personal access token instead of
  a project token; the Trello Power-Up could only be created in a
  workspace the person administered, which is one reason the trigger
  became a comment instead of a button. The person also asked not to add
  lists to the board, so only "In progress" is mapped.

## The machine the factory runs on interferes

- **Look for:** VS Code's git askpass, credential helpers, tokens in the
  shell environment, an SSH agent with many keys, private Go modules.
- **Ask:** nothing usually; detect and work around.
- **BlankTracker:** a `require` on the unpublished Hinziel module made every
  build ask GitHub, which VS Code turned into a login prompt (the factory
  uses a `go.work` instead until Hinziel is published). An SSH agent with
  twelve keys made servers refuse with "Too many authentication failures"
  unless `IdentitiesOnly=yes`.

## Where the factory can run

- **Look for:** the OS of the machines on offer, whether Docker is there,
  and whether anything else runs on a host you'd install on.
- **Ask:** whether a dedicated always-on host is acceptable, and its cost.
- **BlankTracker:** the runner started as a systemd service from a deploy
  script, then moved to `host install`: the factory runs in a container,
  and the agent's sandboxes with Docker inside need Sysbox, so Linux. A
  Mac can't run those sandboxes at all; a factory whose checks need Docker
  is tried on a Linux host from the start. Installing Sysbox restarts
  Docker, which stops whatever else that Docker runs: don't share the host
  with anything that matters.

## Pitfalls in writing the factory itself

- **Look for:** `hinziel.Env` inside funcs the factory holds (read too
  late to be checked), hardcoded values that differ per account (a
  workers.dev subdomain), files only on one machine.
- **BlankTracker:** the factory's source existed only in an uncommitted
  directory and was lost, leaving a binary. Commit the factory early, on
  the branch the team chooses.

## Hinziel's own gaps

When a first run fails, decide whose it is. BlankTracker's first run found
four in Hinziel, each now fixed: an image slug `check` didn't verify, SSH
connections rate-limited by the host, actions that couldn't be confirmed
while the run executed, and an MR description not updated after approval.
Fix these upstream (or record them as issues), not in the factory.
