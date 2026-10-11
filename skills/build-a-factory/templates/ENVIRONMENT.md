# How <app> is deployed

The environment model: every environment of <app>, what it's made of, where
its configuration and data come from, and what a Hinziel preview does
differently. Written by the build-a-factory skill from the evidence noted
next to each fact (`infra`, `script`, `live`, or the person who said so).

## Environments

| Environment | Where | Hostnames | Deployed by | Data |
|---|---|---|---|---|
| production | | | | |
| staging | | | | |
| preview (Hinziel) | | | the factory | |

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
| Where preview data comes from | |
| Side effects a preview must not cause | |
| Resources shared between environments | |
| Secrets in the wrong places | |
| Hostnames baked into the app | |
| Setup only a person can do | |
| The agent's checks need tooling | |
| What the agent can reach | |
| Plan, permission and policy limits | |
| The machine the factory runs on | |

## A preview

What a preview is, and how and why it differs from the environment it
copies.

- **Copies:** <environment>
- **Runs on:** <host type and size, cost per hour and month>
- **Hostnames:** <pattern>
- **Data:** <source, and what it does and doesn't contain>
- **Switched off:** <jobs, integrations, and how>
- **Human actions:** <per preview>
- **How the agent gets in:** <SSH, test user, token>
- **Parity checks:** <what's checked after a deploy>
