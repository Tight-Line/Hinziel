# Learning how the app is deployed

Writing the tracker, forge and agent parts of a factory is mostly
mechanical. Reproducing an environment is not, and it's where the skill
earns its keep. The knowledge of how an app really gets deployed is tacit,
scattered and often partly wrong. BlankTracker was the easy case: its deploy
scripts were in the repository and its owner could explain the rest. Most
teams have none of that written down.

The output of this step is `ENVIRONMENT.md` in the factory repository
(`templates/ENVIRONMENT.md`). For many teams it is the first written
description of their own deployment, worth having even without the factory.

## Dig through the evidence, layer by layer

Each layer is less reliable than the one before it. Read all of them, and
note in the model which layer each fact came from.

1. **Declared infrastructure.** Terraform, Helm charts, compose files,
   Kubernetes manifests, `wrangler.jsonc` and similar. List every file per
   environment: `docker-compose.yml` for local development is not the same
   stack as `vibe.docker-compose.yaml` or `production.docker-compose.yaml`.
2. **Procedures.** CI pipelines and deploy scripts show what really runs,
   in what order, with which variables, and what runs after a deploy:
   migrations, cache resets, permission grants, seeding. Take the factory's
   post-deploy commands from here, in the scripts' order. (BlankTracker:
   `deploy-vibe.sh` ran `migrate --force`, `optimize`,
   `permission:cache-reset`, `give-readonly-permissions-to-all-tables`.)
3. **The running environments, read-only.** DNS records, machines and
   their sizes, running containers and their environment, cron jobs,
   firewall rules, a namespace dump, the cloud console's resources. This is
   where the steps someone once did by hand turn up. Never change anything
   here.
4. **People.** What the evidence doesn't settle becomes a question, one at
   a time, with your recommended answer.

Useful read-only probes, when the person has given you access:

- DNS: list the zone's records (Cloudflare API with a Zone:Read token) and
  note the naming pattern of each environment (`api-staging`, `api-vibe`,
  `nova-vibe`).
- Hosts: `docker ps`, `docker compose config`, `crontab -l`, `ufw status`,
  the `.env` keys in use (names only, never values).
- Workers/Pages/serverless: the list of scripts and their routes.
- OAuth: which client each environment uses, and its redirect URIs. These
  can sit in a different cloud project per environment.

## What differs between environments is the template

Compare staging with production (or vibe with staging) side by side: what
varies per environment (hostnames, databases, keys, sizes, feature flags,
data) and what's fixed. The varying parts become a preview's parameters;
the fixed parts are the same for every preview.

A team with a single environment gives no such comparison. Say so, and ask
more questions instead of guessing.

## Decide what a preview is

Pick the environment a preview copies, usually the cheapest non-production
one that is complete (BlankTracker: vibe, not staging). Then decide, with
the person, each of:

- **Where each part runs.** One host per preview (a droplet), a slot on a
  shared host, a namespace, a serverless preview alias. Note what the
  person's account allows (droplet limits, free-tier caps) and the cost
  per hour and per month of each preview. Look the prices up; don't recall
  them.
- **Hostnames.** Previews need names the app accepts: an HTTPS SPA can't
  call a plain-HTTP API, OAuth providers reject bare IPs, cookies may be
  scoped to a domain. Prefer names one level deep in an existing zone
  (`hz-<ticket>-api.example.com`), which a wildcard certificate covers, and
  restrict the DNS component to that prefix (`AllowedNames: "hz-*"`) when
  the zone also serves production.
- **TLS.** Proxied DNS in Full (strict) mode needs a certificate the proxy
  trusts on the host (a Cloudflare Origin CA certificate for the zone's
  wildcard, for example).
- **Data.** Where a preview's database comes from: migrations and seeds, a
  sanitised dump the team already maintains, a copy of staging. Check what
  the dump actually holds: BlankTracker's vibe dump had reference data
  (users, clients, lookup tables) but no creators or contents, so the
  first ticket's data migration could only be tested on empty tables.
  Tell the person what a preview will and won't have.
- **Side effects.** Every job, integration and outbound call a preview must
  not make (`references/friction.md`). Prefer the app's own kill switches;
  otherwise switch services off in an override (a compose `profiles` entry)
  and keys out of the env template.
- **Human actions.** Setup only a person can do, per preview (OAuth
  redirect URIs). Each becomes an `EnvironmentActions` entry, held back
  only before the step that needs it (usually `publish`, since the agent
  doesn't need it).
- **Access for the agent.** How the agent gets into a preview to test: SSH
  to the host, a token it creates itself (BlankTracker: a Sanctum token from
  `artisan tinker`), a test user. Put this in the factory's
  `Instructions`.

## The machine image and the host

Small facts about hosts break first runs. Check them during planning, not
on the first ticket:

- **The image exists in the region.** Marketplace slugs are easy to get
  wrong: DigitalOcean's Docker 1-Click is `docker-20-04` and runs Ubuntu
  22.04. `check environment` verifies the image now.
- **The host's firewall and SSH limits.** The Docker 1-Click enables `ufw
  limit 22/tcp`: a 7th new SSH connection within 30 seconds is refused.
  Hinziel shares one SSH connection per host and polls slowly enough, but a
  team's own scripts may not.
- **The first build is slow.** BlankTracker's first image build took about
  11 minutes on a 2 vCPU / 4 GB droplet, and compiling PHP extensions would
  run out of memory on 2 GB. Later deploys reuse the build cache (about 40
  seconds). Size hosts for the first build, and tell the person.
- **Published ports.** A preview behind a TLS proxy must not also publish
  the app's port in plain HTTP on the host's IP (`ports: !reset []` in the
  override).

## Write the model before reproducing it

Fill `templates/ENVIRONMENT.md`: every service, what it depends on, where
each piece of its configuration comes from, its external integrations,
where its data comes from, every step done by hand, and what a preview
does differently and why. Ask the person to review it in Phase 1. Keep it
up to date when the first runs teach you something.

## Check parity, not just liveness

An environment that answers on its URLs isn't necessarily faithful to the
one it copies. After the first deploy, check against the model:

- every endpoint responds as the original does (status codes, redirects);
- a user can log in (or the reason they can't yet is a pending human
  action);
- the team's smoke or end-to-end tests pass against it, if they have any;
- every background job and integration either works or is switched off on
  purpose, as the model says;
- migrations ran and the data is what the model promised.

Report the result to the person with the URLs.

## Touch nothing the factory didn't create

Inspecting live environments needs broad read access next to production.
Only read existing environments. The components you choose must follow the
same rule when they write: DNS names under a prefix only, a previews-only
Worker instead of the production one, droplets tagged as the factory's.
