# What a delivered change is, for this team

An agent's change is done when the team would trust it as much as one of
their own. For most teams that takes more than a place to try it. A change
must also pass their checks: lint and unit tests, integration tests, the CI
pipeline on the merge request, smoke or end-to-end tests against a
deployed copy, synthetic checks that watch the running app, a QA pass. The
preview (`references/preview.md`) is one part of a delivered change. This
reference is the rest.

The output is the "A delivered change" section of `ENVIRONMENT.md`: every
check a change must pass, where it runs, who runs it today, and how the
factory will make it happen.

## Start from the last change they shipped

Ask, one at a time:

- **Walk me through everything that happened to the last change between
  "the code is written" and "we trust it".** Commands run locally, CI jobs
  on the MR, a deploy to staging and what someone checked there, a QA
  sign-off, a dashboard someone watched after the merge. A real change
  gets the checks they actually run; "how do you test?" gets the ones
  they wish they ran.
- **Which of those would you insist on for a change nobody wrote by
  hand?** Some teams want more for an agent's change than for their own,
  at least at first. Some checks are rituals nobody would miss.
- **What has got through anyway?** A change that passed every check and
  still broke something shows which check is missing or too weak. Write
  it down even if nobody plans to add the check yet.

## Find every check, including the ones outside the repository

Read the evidence before asking. Checks live in more places than the
`Makefile`:

- **In the repository:** `Makefile` targets, `package.json` scripts,
  `./toolkit check`, pre-commit hooks, `CLAUDE.md` and contributing guides
  ("run X before you finish").
- **In CI:** every job on a merge request, and which ones are required to
  merge (protected branch rules, required status checks). Note jobs that
  only run on the target branch or nightly: an agent's MR never triggers
  them.
- **Against a deployed copy:** smoke tests, end-to-end suites (Playwright,
  Cypress), contract tests between services, load tests. Often in a
  separate repository, or run by hand from someone's laptop.
- **Outside the code:** synthetic checks in a monitoring service
  (Datadog, Checkly, Pingdom), a QA team's suite, a manual test plan in a
  wiki. These often point at fixed hostnames and need work before they
  can point at a preview.

For each check, write down:

| | |
|---|---|
| **What** | the command, job or suite, and what it proves |
| **Where it runs** | the agent's sandbox, CI, against the preview, against staging, in a monitoring service |
| **Needs** | tooling, a dev stack, credentials, a deployed copy, real-looking data |
| **How long** | from a fresh checkout or a fresh preview, timed by you where you can |
| **Reliable?** | flaky tests the team retries by habit; say so, and how the agent should treat a failure |
| **Who runs it today** | a developer, CI, a QA person, nobody since someone left |
| **Required?** | must pass, should pass, or informational |

## Decide how the factory makes each check happen

Today a factory has three ways to run a check. Say plainly which one
each check gets:

1. **The agent runs it while it works.** Lint, unit tests and anything
   that runs in a checkout go in the factory's `Instructions`, with the
   exact command and how long it takes. The agent's sandbox must have
   what the check needs (`SKILL.md`, Phase 2 step 3). Against the preview,
   the agent runs the team's smoke or end-to-end tests after it deploys,
   the same way it tests the change itself. The agent reports what it ran
   and what it couldn't in the MR description.
2. **CI runs it on the merge request,** as it does for the team's own
   changes. The factory opens the MR; the team's pipeline does the rest.
   Make sure an agent's MR triggers the same jobs a developer's does
   (branch name patterns, MRs from bots, approvals needed to run
   pipelines).
3. **A person runs it,** as today. Write it into the ticket comment or the
   MR template so it isn't forgotten, and name who.

Be honest about the limit: in all three, nothing in the factory yet
refuses to call a change ready when a required check failed. The agent is
told to run the checks and report them, and usually does, but the
factory doesn't enforce it. A `verify` step that runs the team's checks
after `deploy` and holds `publish` until they pass is planned (README,
"Checks (planned)"). Mark each required check
that should move to that step once it exists, so the factory can be
upgraded without asking again.

When a check can't run at all yet (a synthetic suite hard-wired to
staging's hostname, an end-to-end suite that needs data a preview
doesn't have), write that down as a gap, with what it would take, and
tell the person what a change will have skipped.

## What must never run

Some checks have side effects: a load test against a shared database, an
end-to-end suite that sends real emails, a synthetic check that places a
real order. Treat them like the side effects in `references/friction.md`:
switched off for previews, or pointed at something safe, on purpose and
written down.
