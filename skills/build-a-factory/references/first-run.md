# Proving the flow with a first run

The first real run proves the factory end to end, and it will find
problems: in the factory, in the environment model, and in Hinziel. Plan
for a few rounds of fix and resume.

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
  comments on the ticket, which the team will see. Get a yes.
- **What the agent needs on this machine**: the local dev stack, if the
  team's checks need it; Claude Code logged in.

## Starting and watching

Start the run in the background (`<factory> run <ticket>`, output to a
log) and follow the steps: `fetch_ticket`, `prepare_workspace`,
`provision_environment`, `deploy_base`, `implement`, `finalize_branch`,
`open_change_request`, `deploy`, `publish`. Tell the person as each step
finishes. During `implement`, the run's log (in
`~/.local/state/hinziel/runs/<id>/log.txt`) has the agent's own
narration; summarise it when asked, and point out anything the person
should know, such as data sources the agent reached.

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

- **Cleanup.** Until `env destroy` exists, a preview keeps running and
  costing money until it's removed by hand: the host, the DNS records, the
  preview deployment, and the human actions to undo (redirect URIs). Say
  so, with the cost, and offer to remove them once the MR is reviewed.
- **Record** what the run taught you in `SETUP.md` and `ENVIRONMENT.md`.
- **Triggers.** Once a run from the CLI works, set up how the team will
  start runs (`<factory> daemon` with a comment trigger, for example) and
  try it with a second ticket. A ticket whose run already finished needs
  iteration support to run again; use a new one.
