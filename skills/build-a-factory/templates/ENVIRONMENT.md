# What a delivered change is for <app>, and how it's deployed

What a change the factory made must pass before it's done, where someone
tries it and why it's that, and the deployment it rests on: every environment of <app>, what it's made of, and
where its configuration and data come from. Written by the
build-a-factory skill from the evidence noted next to each fact (`infra`,
`script`, `live`, or the person who said so).

## A delivered change

Decided with <person>. See the skill's `references/done.md`.

- **Today:** <the checks the last change passed, from "written" to "trusted", and who ran each>
- **Got through anyway:** <a change that passed everything and still broke something, and what would have caught it>

| Check | Proves | Runs where | Run by | Takes | Required? | Under `verify` once it exists? |
|---|---|---|---|---|---|---|
| <`make lint test`> | <…> | <agent's sandbox> | <the agent> | <3 min> | <must pass> | <yes> |
| <CI pipeline on the MR> | <…> | <CI> | <CI> | <12 min> | <must pass> | <yes, via CI status> |
| <Playwright suite> | <…> | <against the preview> | <the agent> | <8 min> | <must pass> | <yes> |
| <Checkly synthetics> | <…> | <monitoring, staging only> | <nobody for previews yet> | | <should pass> | <gap: hard-wired to staging> |

- **Can't run yet, and what it would take:** <…>
- **Must never run against a preview:** <checks with side effects, and how they're switched off>

## A preview

Decided with <person>.

- **Today:** <how a change gets tried now, by whom, and what's wrong with it>
- **Must check:** <what the person trying a change needs: the UI, real-looking data, an integration, …>
- **Shape:** <the team's staging | a light copy: folder and vhost, namespace, review app | a full environment per ticket>, because <reason>
- **Gives up:** <what this shape gives up, as agreed>
- **Is / copies:** <staging itself, or the environment a preview copies>
- **Hostnames:** <staging's fixed URLs, or the pattern per preview>
- **Data:** <what a preview has and doesn't, and what the agent may change>
- **Switched off:** <jobs, integrations, and how; "nothing" on staging>
- **How the agent gets in:** <SSH, test user, token>
- **Checked after a deploy:** <what makes it faithful>

For the team's staging:

- **Deployed by:** <the team's script or CI job, its inputs, what runs after it>
- **Others who deploy there, and how they coordinate:** <…>
- **Back to the target branch:** <how>

For a light copy or a new environment:

- **Runs on:** <shared host, cluster, or a host per preview; size; cost per hour and month>
- **Shares:** <database, queue, bucket, … and what that means>
- **Human actions:** <per preview>

## Still unknown

What nobody has settled yet, who can answer it, and what it blocks.

- <question> (ask <who>; blocks <what>)

## Environments

| Environment | Where | Hostnames | Deployed by | Data |
|---|---|---|---|---|
| production | | | | |
| staging | | | | |
| preview (Hinziel) | <staging, a slot, or its own> | | the factory | |

## Services

For each service: what it is, where it runs, what it depends on, where its
configuration comes from.

### <service>

- **Runs:** <compose service, Worker, host, …>
- **Built from:** <Dockerfile target, build script>
- **Configuration:** <env file, variables, secrets store> (source)
- **Depends on:** <database, cache, other services>
- **After a deploy:** <commands, in order> (source)
- **Integrations:** <external APIs, mail, webhooks>

## What varies per environment

| Setting | production | staging | preview |
|---|---|---|---|
| | | | |

## Steps done by hand

Every step someone does outside a script, per environment and once.

- <step> (who, when, how often)

## Friction categories

One line each, "not applicable" included (see the skill's
`references/friction.md`).

| Category | Here |
|---|---|
| The team's rules for agents and changes | |
| Deploy knowledge spread around | |
| What a preview can be | |
| Where preview data comes from | |
| Side effects a preview must not cause | |
| Resources shared between environments | |
| Secrets in the wrong places | |
| Hostnames baked into the app | |
| Setup only a person can do | |
| The agent's checks need tooling | |
| Checks that live outside the repository | |
| What the agent can reach | |
| Plan, permission and policy limits | |
| The machine the factory runs on | |
