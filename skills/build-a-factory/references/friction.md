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
  them while the agent worked; `approve` now confirms an action at any
  time, so doing it early never holds the run up.

## The agent's checks need tooling the preview lacks

- **Look for:** what the team's "before you finish" command needs: a local
  dev stack, Docker, Node in a container, a database.
- **Ask:** where the agent should run those checks.
- **BlankTracker:** `./toolkit check` needs the local dev stack. The agent
  ran `./toolkit init` in its own checkout, which gave it a stack on its
  own ports next to the developer's. On a server or in a container, that
  needs Docker there too: unsolved for hosted factories.

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
