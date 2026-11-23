# What a preview is, for this team

A preview is the part of a delivered change where someone can try it.
The checks a change must also pass are in `references/done.md`; settle
both in Phase 1, since some checks run against the preview and constrain
what it must be.

An agent's change is worth something only once someone can try it. Where
that is differs for every team: the staging they already deploy branches
to, a slot on shared infrastructure, a scratch org, or an environment of
its own per ticket. Working this out with the team is the hard part of
installing Hinziel and where the skill earns its keep. Writing the
tracker, forge and agent parts of a factory is mostly mechanical.

Never start from "an environment per ticket". Start from how the team
tries changes today, learn how the app is really deployed, and only then
recommend what a preview should be. Often the answer is what they already
do by hand, done for them. A new environment per ticket is one answer
among several, the most faithful and the most work.

The output of this step is `ENVIRONMENT.md` in the factory repository
(`templates/ENVIRONMENT.md`): what a preview is for this team and why,
and the deployment it rests on. For many teams it is the first written
description of their own deployment, worth having even without the
factory.

## Start from how they try changes today

Before reading any infrastructure, ask, one at a time:

- **Walk me through the last change you tried before it was merged.**
  From the ticket to someone trying it: where it ran, who deployed it and
  how, who looked at it, what they checked. A real, recent change gets
  the process they follow; "how do you test changes?" gets the one they
  believe in. A branch deployed to staging by hand, a review app per MR,
  a scratch org, only locally?
- **What must the person trying it check?** The UI, real-looking data, an
  integration, performance, a workflow across several services. That sets
  how faithful a preview must be, and what it may leave out.
- **What's wrong with how it works now?** Waiting for staging, changes
  overwriting each other, a reviewer who can't run it locally. This says
  what a better preview would be worth to them.

Whatever they do now is the baseline. A preview no less useful than the
baseline, made without a person, may be all they need.

## Learn how the app is deployed

The deployment shows which previews are possible and what each would
take. The knowledge is tacit, scattered and often partly wrong.
BlankTracker was the easy case: its deploy scripts were in the
repository and its owner could explain the rest. Most teams have none of
that written down.

Dig through the evidence layer by layer. Each layer is less reliable
than the one before it. Read all of them, and note in the model which
layer each fact came from.

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

Compare the environments side by side (staging with production, vibe
with staging): what varies per environment (hostnames, databases, keys,
sizes, feature flags, data) and what's fixed. Should a preview copy an
environment, the varying parts become its parameters and the fixed parts
are the same for every preview. A team with a single environment gives no
such comparison. Say so, and ask more questions instead of guessing.

## Find what isn't written down

The evidence and the person's first answers leave out what everyone on
the team knows and nobody wrote down: the step someone always does after
a deploy, the service that must never be called from staging, the one
person who knows how the certificates get renewed. A factory that misses
these fails on its first run, or worse, succeeds and does harm. Finding
them is the most valuable thing this skill does. Go through every
category in `references/friction.md`, and probe:

- **Where the account and the evidence disagree.** The person says
  deploys go through CI, but the server has a `deploy.sh` with recent
  timestamps; the compose file names a service nobody mentioned; DNS has
  a record no Terraform creates. Say what you found and ask which is true.
  The gap is usually a step done by hand.
- **For traces of hand work.** Resources not declared anywhere, cron
  jobs, `.env` keys with no source, a wiki page or a pinned Slack message
  that says "after deploying, remember to…". Ask who does it, when, and
  what happens when they forget.
- **What has gone wrong.** "When did a change look fine in review and
  break after merging?" shows what a preview must be faithful to. "What
  do new people get wrong in their first month?" shows what's only in
  people's heads.
- **What must never happen.** "What would you be upset to find a preview
  had done?" Emails to real customers, a charge on a real card, a
  webhook into a partner's production, a write to a shared database.
  These become what a preview switches off.
- **Who else knows.** Some answers aren't the person's to give: ops owns
  the cluster, another team owns the SSO, one developer always does the
  deploys. Say who you think knows, write down exactly what to ask them,
  and carry on with what can be settled now. Don't guess in their place.
- **A guess is easier to correct than a blank to fill.** "I think staging
  gets its data from a weekly dump of production, because of the
  `restore-staging` cron job; is that right?" gets a better answer than
  "where does staging's data come from?".

Record the unknowns too: what's still open, who can answer it, and what
it blocks. An honest "nobody knows how the OAuth client was set up" is
worth more to the team than a confident wrong model.

## Decide with the team what a preview is

The evidence shows what's possible; the team decides what's worth it.
Two more questions, one at a time:

- **Can an environment be created at all?** A licensed product with one
  test instance, an appliance or a mainframe, a partner that allows a
  single host, or data that can't be copied rules it out.
- **How many tickets are in flight at once,** and what may a preview per
  ticket cost? Look the prices up; don't recall them.

Then recommend one shape, say what each gives up, and let the person
choose (README §11 has a scenario for each):

| Shape | Fits when | Gives up |
|---|---|---|
| **The staging the team already has** (scenario E) | environments can't be created, one ticket at a time is enough, or it's how they work today and works well enough | the last deploy wins: another ticket or a developer overwrites it; data changes stay; one ticket at a time |
| **A light copy on shared infrastructure**: a folder and vhost (B), a namespace (D), a review app | the app runs as a slot next to others, with shared backing services | faithfulness where it shares: one database, one queue |
| **A full environment per ticket** (A, C) | the person trying it needs it faithful and isolated, and the team can pay per ticket | the most to build, run and pay for |

When the choice is close, recommend the smaller one: a factory that
deploys to staging can move to an environment per ticket later, once the
team has seen what agents' changes are like.

Write the choice and the reasons into `SETUP.md`'s decisions and the
"A preview" section of `ENVIRONMENT.md`. Then work out the details of the
chosen shape, below.

## A preview on the team's staging

The factory provisions nothing: a provisioner returns staging as it is
(its fixed URLs and hosts), `Destroy` does nothing, there's no DNS and no
`Teardown`. The deployers are the team's own staging deploy, run for the
run branch's commit. Find out, and write down:

- **How a branch gets to staging today,** exactly: the script or CI job,
  its inputs, what runs after it. The factory's deployer does the same.
- **Who else deploys there, and how they coordinate.** A Slack message, a
  lock, nothing. Recommend `daemon --runs 1`, and say plainly that a
  developer's deploy still replaces the agent's, and the agent's theirs.
- **How staging gets back to the target branch.** A new run for another
  ticket, the next developer's deploy, or a deploy the person triggers.
- **What the agent may do to staging's data.** Migrations and tests'
  writes stay after the code is replaced. Put the rule in the factory's
  `Instructions`, and have the agent test locally first, in a sandbox
  with the dev stack if the team has one.
- **What the team hears.** The notifications say which ticket staging now
  runs, so nobody reviews the wrong change.

This is the one place a factory writes to an environment it didn't create.
Do it only through the team's own deploy, and only after the person has
said yes to it.

## A light copy on shared infrastructure

A preview is a slot next to the others: a folder and a vhost on a shared
host, a namespace in a preview cluster, a review app on the team's
platform. The provisioner creates the slot and `Destroy` removes it; the
host or cluster itself is the team's, and the factory never changes it.
Find out, and write down:

- **Where the slots live** and how one is named (`<ticket>.preview.example.com`,
  `hz-<ticket>`). A wildcard DNS record and certificate usually cover them
  all, so no DNS per preview is needed.
- **What a slot shares** with the others and with staging: the database,
  a queue, a bucket, a cache. Say what that means for the person trying
  it (one ticket's migration is every ticket's migration), and what a
  slot gets of its own (a schema, a key prefix).
- **How many slots fit,** and what happens to one whose ticket is done.
- **How the team's own review apps are made,** if they have them: the
  factory's slot should be made the same way.

The parts of the next section about hostnames, data, side effects and
human actions apply to a slot too, for whatever it doesn't share.

## A new environment per ticket

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
  trusts on the host. Prefer one per environment from an issuer
  (`dockercompose.TLS.Issuer` with `cert/cloudflare`): every preview runs
  unreviewed code, and a shared key, worse a wildcard for the production
  zone, is exposed on all of them.
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

### The machine image and the host

For previews on hosts of their own. Small facts about hosts break first
runs. Check them during planning, not on the first ticket:

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

## Write it down before building anything

Fill `templates/ENVIRONMENT.md`: how changes get tried today, what a
preview is for this team and why, what's still unknown, then every
service, what it depends on, where each piece of its
configuration comes from, its external integrations, where its data comes
from, every step done by hand, and what a preview does differently and
why. Ask the person to review it before planning the factory. Keep it up
to date when the first runs teach you something: every run that fails on
something nobody mentioned found another thing that wasn't written down.

## Check the preview is faithful, not just that it answers

A preview that answers on its URLs isn't necessarily one the person can
rely on. After the first deploy, check it against the model: a new
environment or a slot against the environment it copies; the team's
staging against how their own deploy of the target branch leaves it.

- every endpoint responds as the original does (status codes, redirects);
- a user can log in (or the reason they can't yet is a pending human
  action);
- the checks `ENVIRONMENT.md` runs against a deployed copy (smoke,
  end-to-end, synthetic) pass against it, or the reason one can't run
  yet is written down;
- every background job and integration either works or is switched off on
  purpose, as the model says;
- migrations ran and the data is what the model promised.

Report the result to the person with the URLs.

## Touch nothing the factory didn't create

Inspecting live environments needs broad read access next to production.
Only read existing environments; the team's staging, when it's the
preview, is the one agreed exception, and only through their own deploy.
The components you choose must follow the same rule when they write: DNS
names under a prefix only, a previews-only Worker instead of the
production one, droplets tagged as the factory's, slots on shared
infrastructure and nothing else there.
