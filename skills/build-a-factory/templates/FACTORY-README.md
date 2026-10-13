# <factory>: Hinziel factory for <app>

This directory is <app>'s software factory, built with
[Hinziel](https://github.com/Tight-Line/hinziel). It takes a ticket from
<tracker>, has <agent> implement it on a `hinziel/*` branch, deploys the
branch to a preview of its own and opens a merge request into
<target branch>.

How the app is deployed, and how a preview differs: `ENVIRONMENT.md`.
How the factory was installed and why: `SETUP.md`.

## Running it

```sh
set -a; . ~/.config/hinziel/<factory>.env; set +a
go build -o <factory> .
./<factory> check                 # credentials and tools, all at once
./<factory> run <ticket>          # one run, in the foreground
./<factory> daemon                # <how runs start: e.g. "@<member> implement" comments>
./<factory> status                # runs so far
./<factory> approve <run-id>      # confirm human actions, pass a gate
./<factory> env list              # previews that exist
./<factory> env destroy <ticket>  # tear one down
```

## Where it runs

<host, size and cost>, installed and updated with `<script>` (Hinziel's
`host install`): the factory runs there as the container
`hinziel-<factory>`, up to <N> runs at once.

- Logs: `ssh <host> docker logs -f hinziel-<factory>`
- Runs and their logs: `/var/lib/hinziel/<factory>/state/runs/`
- Configuration: `/etc/hinziel/<factory>/env` (root only)

The agent works <in the factory's container | in a sandbox per run: its contents: Docker, extra tools>.

## What a run creates

- <host per preview, size and cost>
- <DNS records>
- <preview deployments>
- a branch `hinziel/<ticket>-…`, a merge request, comments on the ticket

<When previews are torn down: on `env destroy`, a "teardown" comment, or
once the ticket is done.>

## Human actions

<Per preview, what someone must do, and how to undo it.>

## Credentials

| Variable | What | Owner | Least access | Expires | Rotate by |
|---|---|---|---|---|---|
| | | | | | |

## Files

- `main.go`: the factory
- <override files, env templates, and what each is for>
