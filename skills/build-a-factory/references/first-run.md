# Proving the flow with a first run

The first real run proves the factory end to end, and it will find
problems: in the factory, in Hinziel, and in what `ENVIRONMENT.md` says
about how the team tries changes. Plan for a few rounds of fix and resume.

## Before starting

- **The full `check` passes.**
- **Choose the ticket with the person.** Small and low-risk, but real
  enough to exercise the stack: BlankTracker's first ticket touched Nova,
  migrations, existing data and the SPA, which showed far more than a typo
  fix would have. Read it first and say what you expect the agent to
  touch, and what the preview's data allows testing (a ticket about
  existing records needs records).
- **Say what the run will create**: a host (with its hourly and monthly
  price), DNS records, a preview deployment, a branch and an MR, and
  comments on the ticket, which the team will see. On the team's staging,
  say instead that the run replaces what staging runs, and agree the
  moment, so nobody is reviewing something else there. Get a yes.
- **What the agent needs**: `check` passes its `sandbox` line, if it has
  a sandbox (the runtime is there and the sandbox's image builds), and its
  `agent` line (a Claude token in the env file, which the agent needs even
  on a logged-in laptop; without a sandbox, also Claude Code installed on
  the host).

## Starting and watching

Start the run in the background (`<factory> run <ticket>`, output to a
log) and follow the steps: `fetch_ticket`, `prepare_workspace`,
`provision_environment`, `deploy_base`, `implement`, `finalize_branch`,
`open_change_request`, `deploy`, `publish`. Tell the person as each step
finishes. During `implement`, the run's log has the agent's own narration
(`runs/<id>/log.txt` in the state directory: `~/.local/state/hinziel` on a
laptop, `/var/lib/hinziel/<factory>/state` on a host from `host
install-daemon`); summarise it when asked, and point out anything the person
should know, such as data sources the agent reached. The agent's sandbox,
if it has one, is the container `hinziel-<factory>-<run id>` while it works. Its first
check may take long while it builds the dev images, unless the team shares
them through a registry; that's expected, not a hang.

Human actions are announced right after provisioning. Pass them on as soon
as they appear, with exact values, so the person can do them while the
agent works, and confirm them with `approve <run-id>` once done: the run
then doesn't stop for them.

## When a step fails

Read the error and the run's log, and find the cause before changing
anything. Decide whose problem it is:

- **The factory's**: a wrong option, a missing file. Fix it, rebuild, and
  `resume <run-id>`; the run continues from the failed step, and the steps
  are idempotent.
- **Hinziel's**: a component that should have caught it in `check`, a
  missing feature. Fix it upstream with a test, then resume.
- **The host's or the provider's**: a firewall limit, an image slug.
  Inspect read-only, then decide.

Failures post comments on the ticket. Tell the person, so they can tidy the
ticket before the team reads it.

## Verifying the result

When the run succeeds, check with the person:

- the MR: title, description (what changed, testing, manual steps, human
  actions shown as done), target branch;
- the preview: each URL answers, the change is visible, login works or the
  reason it doesn't is known;
- the ticket: moved to the in-progress stage, comment with the MR and the
  preview;
- what the agent couldn't test, as it says in the MR (BlankTracker's
  data-mapping migration only ran on empty tables), and the manual steps it
  lists.

## Afterwards

- **Teardown.** On the team's staging there's nothing to tear down: say
  what staging runs now, and how it gets back to the target branch. A
  preview of its own keeps running and costing money until it's torn
  down: by `env destroy <ticket>`, by a "teardown" comment, or by the
  daemon once the ticket is done (`Factory.Teardown`). Say which applies,
  with the cost, and list the human actions to undo by hand (redirect
  URIs); the teardown's notification lists them too.
- **Record** what the run taught you in `SETUP.md` and `ENVIRONMENT.md`.
- **Triggers.** Once a run from the CLI works, set up how the team will
  start runs (`<factory> daemon` with a comment trigger, for example, on
  the host from `host install-daemon`) and try it with a second ticket. A ticket whose run already finished needs
  iteration support to run again; use a new one.
