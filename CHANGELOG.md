# Changelog

Every change a user of CairnPost can notice is written here. The format is
[Keep a Changelog](https://keepachangelog.com/en/1.1.0/) and versions follow
[Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

### Added

- `POST /emails`: send one email to one recipient with an API key. A request
  with an `Idempotency-Key` repeated with the same content is one email.
- `GET /emails/{id}`: what became of an email (`sending`, `sent`, `deferred`,
  `delivered`, `bounced`, `complained`, `failed`, `unknown`) and each event
  that happened to it.
- Delivery events: with `CAIRNPOST_CLOUDFLARE_QUEUE_ID` set, CairnPost reads
  what Cloudflare says became of each email and records it.
- Suppression: an address that bounces for good or complains is no longer
  sent to from that sending domain; a send to it answers 422
  `recipient_suppressed`. A refusal that is about the sender (status class
  5.7) suppresses nobody. `GET /suppressions`, `POST /suppressions`,
  `POST /suppressions/lift` and `cairnpost suppressions list|add|remove`
  show the list, add to it by hand, and lift one entry or a whole domain's.
  A lift is remembered, so a late bounce of an older email does not undo it.
- Webhooks: `POST /webhooks` has a URL told what becomes of emails
  (`email.delivered`, `email.delivery_delayed`, `email.bounced`,
  `email.complained`, `email.failed`), signed as Standard Webhooks and Svix
  sign, retried on a schedule (`CAIRNPOST_WEBHOOK_*`). Endpoints are listed,
  changed, disabled, removed and tried out; their deliveries are listed and
  a webhook given up, or waiting, tried again by hand. `cairnpost webhooks create|list|remove`.
- An endpoint whose every try has failed for
  `CAIRNPOST_WEBHOOK_PAUSE_AFTER` (5 days by default, `off` for never) is
  paused when a webhook is given up: it is still owed what happens, and
  nothing is sent to it until `POST /webhooks/{id}/resume`, which also
  sends what was given up while it failed. `paused_at` and `failing_since`
  say how an endpoint stands.
- `POST /webhooks/{id}/secret` changes an endpoint's signing secret. For
  `CAIRNPOST_WEBHOOK_SECRET_OVERLAP` (24 hours by default) webhooks are
  signed with the old and the new one both.
- `address.suppressed`: a webhook for an address suppressed because of a
  bounce or a complaint about an email with no record, which had no
  webhook to tell of it.
- API keys with a scope (`send` or `admin`), optionally bound to one sending
  domain; stored hashed, shown once. `cairnpost keys create|list|revoke`.
- The `cloudflare` driver: delivery through Cloudflare Email Sending.
- The `capture` driver, for development: it keeps emails in memory, sends
  nothing, and shows them at `GET /capture/emails` to an admin key.
- `GET /limits`: the provider's sending allowance and how much is used.
- `GET /health`: the service, its database, the driver, the version, and
  whether delivery events are being read, webhooks sent and old records
  pruned.
- Pruning: an email's record, with its events and webhooks, is deleted
  `CAIRNPOST_RETENTION` after the last thing that happened to it (30 days
  by default, 7 at least, `off` to keep for ever), and an
  `Idempotency-Key` when its life is over. A suppression in force is never
  deleted; a lifted one is forgotten once no email sent before the lift is
  on record.
- A send left `sending` because the server stopped in the middle of it is
  set to `unknown` within twice `CAIRNPOST_IN_DOUBT_WINDOW`, instead of
  staying `sending` for good. The log says how many, as a warning.
- A path the API does not have answers 404 `route_not_found`, apart from
  the `not_found` of a record that is not there, so a wrong server address
  is not read as a missing email. A path that is there under another
  method answers 405 `method_not_allowed` with `Allow`.
- Content retention, off by default: with `CAIRNPOST_CONTENT_RETENTION` set
  (30 days at most), what each email said, and what the receiving server
  said about a bounce or a delay, is kept for that long and shown by
  `GET /emails/{id}` to an admin key. Never logged, never in a webhook.
- A console at `/console`, served by the same binary: sign in with an
  admin key to see whether each part of the server is working and to list,
  make and revoke API keys. `CAIRNPOST_CONSOLE`, `CAIRNPOST_CONSOLE_SESSION`
  and `CAIRNPOST_CONSOLE_PLAIN_HTTP` set it up.
- The console lists the emails on record, newest first, narrowed to a
  state or a recipient, and shows what happened to each and, where content
  is kept, what it said. A recipient looked for is never in a page's
  URL, which carries a handle for it, and each tab keeps its own search.
- The console shows the suppression list, finds an address on it, adds
  one by hand and lifts one or a sending domain's, asking first.
- `cairnpost domains check DOMAIN` and the console's Domains page look up
  a sending domain's DNS: the records the mail provider asks for, and its
  DMARC policy. The command needs neither the database nor the provider,
  takes `-provider`, and fails when the domain is not ready. A bounce
  address that points anywhere but at the provider's mail servers is not
  right. `CAIRNPOST_DNS_SERVER` names the DNS server to ask, for a machine
  whose own resolver does not answer as the public internet does.
- The console manages webhook endpoints: make, change, turn off and on,
  send a test, remove, and see and retry what each was sent.
- An admin key bound to a sending domain reads that domain's emails only,
  at `GET /emails/{id}` as in the console.
- An address in `from` or `to` longer than 320 bytes is refused with a 422.
- A key's name is plain text on one line, a hundred characters at most,
  and its sending domain a host name; a sending domain holds
  `CAIRNPOST_MAX_KEYS` live keys (200). A key made in the console records
  which key made it.
- Send counts: how many emails were sent each hour from each sending
  domain, and what became of them, counted apart from the emails' records
  and kept for `CAIRNPOST_COUNTS_RETENTION` (62 days) whatever
  `CAIRNPOST_RETENTION` is.
- `cairnpost serve`, `migrate`, `send` and `version`; a Dockerfile and a
  Compose file with Postgres 18.
- Images for amd64 and arm64 at `ghcr.io/mettjs/cairnpost`, published with
  each release.
