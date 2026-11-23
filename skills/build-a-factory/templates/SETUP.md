# Hinziel installation: <factory>

Progress of installing Hinziel for <app>. Kept by the build-a-factory skill
so that any session can pick up where the last one stopped. Decisions are
recorded with the reason and who made them.

## Decisions

| Date | Decision | Why | By |
|---|---|---|---|
| | Factory lives in <dedicated repo / `hinziel/` in the monorepo> | | |
| | A preview is <the team's staging / a light copy / a full environment per ticket>, copying <environment> | | |
| | Runs start from <CLI / comment trigger / …> | | |
| | The agent's sandbox: <none / base image / Docker, tools> | | |
| | The factory runs on <host>, <N> runs at once | | |
| | Commits: <who branches, commits and pushes> | | |

## Phase 1: find out what delivering a change takes

- [ ] Repositories and the team's process as written
- [ ] How changes get checked and tried today
- [ ] How the app is deployed, from the evidence
- [ ] Friction categories gone through (see `ENVIRONMENT.md`)
- [ ] What a preview is, decided
- [ ] What a delivered change is: every check, and who runs it
- [ ] `ENVIRONMENT.md` reviewed by <person>

## Phase 2: plan the factory

- [ ] What the agent may reach, decided
- [ ] Components chosen
- [ ] Triggers and notifications chosen
- [ ] Plan confirmed

## Phase 3: build and prove

- [ ] Factory written, builds
- [ ] Env file written: `~/.config/hinziel/<factory>.env`
- [ ] Credentials, each verified:
  - [ ] <component>: <what> (<verified facts: owner, scopes, expiry>)
- [ ] Full `check` passes
- [ ] Preview of the target branch up, checked it's faithful
- [ ] First run: ticket <id>, run <run id>, MR <link>
- [ ] Installed on <host> with `host install-daemon`; check passes there
- [ ] Handover README written

## Open items

- <expiry dates to renew, previews still running, follow-ups>
