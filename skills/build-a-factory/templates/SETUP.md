# Hinziel installation: <factory>

Progress of installing Hinziel for <app>. Kept by the build-a-factory skill
so that any session can pick up where the last one stopped. Decisions are
recorded with the reason and who made them.

## Decisions

| Date | Decision | Why | By |
|---|---|---|---|
| | Factory lives in <dedicated repo / `hinziel/` in the monorepo> | | |
| | Previews copy <environment> | | |
| | Runs start from <CLI / comment trigger / …> | | |
| | The agent's sandbox: <none / base image / Docker, tools> | | |
| | The factory runs on <host>, <N> runs at once | | |
| | Commits: <who branches, commits and pushes> | | |

## Phase 1: plan

- [ ] Repositories and the team's process
- [ ] Environment model (`ENVIRONMENT.md`) reviewed by <person>
- [ ] Friction categories gone through (see `ENVIRONMENT.md`)
- [ ] What the agent may reach, decided
- [ ] Components chosen
- [ ] Triggers and notifications chosen
- [ ] Plan confirmed

## Phase 2: build and prove

- [ ] Factory written, builds
- [ ] Env file written: `~/.config/hinziel/<factory>.env`
- [ ] Credentials, each verified:
  - [ ] <component>: <what> (<verified facts: owner, scopes, expiry>)
- [ ] Full `check` passes
- [ ] Environment for the target branch up, parity checked
- [ ] First run: ticket <id>, run <run id>, MR <link>
- [ ] Installed on <host> with `host install`; check passes there
- [ ] Handover README written

## Open items

- <expiry dates to renew, previews still running, follow-ups>
