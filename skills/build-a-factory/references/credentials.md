# Collecting credentials

Collecting credentials is the most tedious part of an installation, and a
missing one stalls everything after it. Done as a guided conversation, it's
also the part people enjoy most. Keep to the loop below.

## The loop

For each item, in the env file's order:

1. **Say what it's for, in one line**, and whether it's a secret.
2. **Give the click path**: the exact URL, then each menu, button and field,
   with the value to type or choose. Give the least access that works and
   say why each permission is needed. Name the thing so it's recognisable
   later (`hinziel-<factory>`).
3. **Wait for "saved".** If the person sends a screenshot, check it against
   what you asked for, and map your steps onto the screen when the
   console has changed.
4. **Verify it, read-only, without printing it.** Load the env file into
   the command's environment and call the service: who owns the token, its
   scopes, its expiry, and whether it reaches the resource the factory
   uses. Report each fact. Once the factory builds, use
   `<factory> check <component>`; a selected check only notes variables
   still empty elsewhere.
5. **Fill in what you can now look up.** A working token often reveals the
   next values: an account ID, a subdomain, a project. Write non-secret
   values into the env file yourself and say so.
6. **Mention what matters later, briefly**: an expiry date to renew, a
   token that belongs to a person rather than the account, a token whose
   reach is wider than the factory needs, a secret that showed up in a
   screenshot.

Then the next item. When the person wants to skip one, say what it costs
and adjust the factory.

Never paste secrets into the chat or ask for them there. A secret the
person pastes anyway: don't repeat it, suggest rotating it if it matters.
A value the provider documents as public (Trello's API key) can be taken
from a screenshot.

## Accounts for the factory

Ask whether the factory should act as a bot user or as the person.

- A **bot** makes the factory's comments, branches and MRs recognisable,
  and limits its reach to what it's added to. It costs a seat on plans
  that count members.
- The **person's own token** is quickest for a first run; switching to a
  bot later only means replacing the token.

BlankTracker used a bot member on Trello (only on the one board, so the
token reaches nothing else) and the person's own GitLab token (free tier,
five members per private group).

## Notes per service

Verify with the calls given; all are read-only.

### Trello (tracker/trello, trigger/trellocomments)

- A member is a full Atlassian account; there are no bot accounts. Sign up
  a new account in a private window, then invite it from the board's
  **Share** button as a Member. Board only, not the workspace.
- API key: https://trello.com/power-ups/admin → **New**. The form asks
  whether the app uses Power-Up capabilities, and that can't be changed
  later. For the API key alone, choose "doesn't use Power-up capabilities".
  The workspace dropdown only
  lists workspaces the person administers, and an unlisted Power-Up only
  works on boards in its own workspace.
- The key is public by Trello's design. The token is not: authorize it **as
  the bot**, by opening this in the bot's session, not the "Token" link on
  the key page (that authorizes whoever is logged in):
  `https://trello.com/1/authorize?expiration=never&scope=read,write&response_type=token&name=Hinziel&key=<KEY>`
- Verify: `GET /1/members/me` (the bot's username), `GET
  /1/tokens/<token>?fields=permissions,dateExpires`, `GET /1/boards/<board>?lists=open`
  (the lists the factory maps exist).

### GitLab (forge/gitlab)

- Token: avatar → Edit profile → Access tokens; scopes `api` and
  `write_repository`; the longest expiry allowed. Free tier has no project
  access tokens.
- Branches `hinziel/*` must not match a protected-branch rule (a `*` rule
  would).
- Verify: `GET /api/v4/personal_access_tokens/self` (scopes, expiry), `GET
  /api/v4/projects/<project>/members/all/<user id>` (Developer or more), `GET
  /api/v4/projects/<project>/protected_branches`.
- Tell the person the expiry date: the factory stops working on it.

### DigitalOcean (env/digitalocean)

- Token: API → Tokens → Generate New Token → Custom scopes: `droplet`
  create/read/delete, `tag` create, `ssh_key` read, `account` read. DO adds
  `tag` read itself.
- SSH key: generate one for the factory (`ssh-keygen -t ed25519 -N "" -f
  ~/.config/hinziel/id_ed25519`), and have the person add the `.pub` under
  Settings → Security → SSH Keys **in the team the token belongs to**. The
  public key is safe to show in the chat.
- Verify: `GET /v2/account` (team, droplet limit), `GET
  /v2/account/keys/<fingerprint>`, `GET /v2/images/<slug>` (exists in the
  region).

### Cloudflare DNS (dns/cloudflare)

- Token: My Profile → API Tokens → Create Custom Token: Zone → DNS → Edit
  and Zone → Zone → Read, limited to the one zone. It belongs to the
  person, not the account: it stops working if they leave the account. An
  account-owned token (Manage Account → API Tokens) outlives them.
- Verify: `GET /client/v4/user/tokens/verify`, `GET /zones?name=<zone>` (this
  also gives the **account ID**: fill it in), `GET
  /zones/<id>/dns_records` (count, naming patterns).

### Cloudflare Origin CA (deploy/dockercompose TLS)

- The zone → SSL/TLS → Origin Server → Create Certificate; defaults are
  right (RSA 2048, `*.zone` and the apex, 15 years). The person saves the
  certificate and the key to two files; the key is shown once.
- Creating one changes nothing for existing servers or zone settings; only
  revoking a certificate or changing the SSL mode would.
- Verify: `openssl x509 -noout -subject -dates -ext subjectAltName`, and
  that the key's public key matches the certificate's. `chmod 600` both.

### Cloudflare Workers (deploy/cfworkers)

- Token: Create Token → "Edit Cloudflare Workers" template, limited to the
  account and zone. `Workers Scripts:Edit` covers every Worker in the
  account, production's too: say so.
- Verify: `GET /accounts/<id>/workers/subdomain` (fill in the workers.dev
  subdomain; BlankTracker's was `ante-4a0`, not the app's name), `GET
  /accounts/<id>/workers/scripts` (the existing Workers; the preview
  Worker is created on first deploy).

### Google OAuth (an app with Google login)

- Find which client each environment uses (`.env.example`, deployed
  `.env` key names) and in which project; production may use another
  project entirely. Offer a dedicated "previews" client; reusing the dev
  client is fine if the person prefers it.
- Redirect URIs can't be added by API. Avoid a human action per preview
  with the OAuth relay (`deploy/cfoauthrelay`): register its callback
  URLs once (`https://<relay host>/<callback path>` for each callback the
  app has) and set the app's redirect host to the relay on every
  preview. Google says changes take from five minutes to a few hours.
- A consent screen in Testing mode only lets listed test users log in.

### Slack (notify/slack)

- api.slack.com/apps → Create New App → From scratch → Incoming Webhooks →
  Add New Webhook, one per channel. Optional: without Slack, problems and
  human actions still go on the ticket.

### The agent (agent/claudecode)

- Always a token in the env file: `CLAUDE_CODE_OAUTH_TOKEN` from `claude
  setup-token` (a subscription), or `ANTHROPIC_API_KEY`. The agent runs in
  its sandbox, where the person's own login isn't, so a logged-in laptop
  isn't enough. `check agent` says when neither is set.
