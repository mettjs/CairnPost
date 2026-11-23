# Changelog

Every change a user of CairnPost can notice is written here. The format is
[Keep a Changelog](https://keepachangelog.com/en/1.1.0/) and versions follow
[Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

## [0.1.2] - 2026-11-22

### Fixed

- A wait is never written longer than 999,999,999 seconds (about 31
  years) in a `Retry-After` header or in a batch's `retry_after`. A
  longer one, which only a mail provider's own answer could ask for, was
  written in ten digits, and the clients read that as no wait at all.
- With the `cloudflare` driver, a wait Cloudflare says in a number too
  large to count is read as the longest wait there is. It wrapped round
  into no wait, or into a wait of one second.
- The server holds back on its mail provider's word for an hour at
  most, or for what the new setting `CAIRNPOST_MAX_PROVIDER_WAIT` says
  (`1m` to `168h`). When the provider says its limit is reached, the
  scheduled emails that are due are put off and the reader of delivery
  events rests, for as long as the provider asked, whatever it asked:
  one wrong answer asking for years stopped both for years, and the
  emails stayed put off through a restart. Now the provider is tried
  again when the hour is over, so a provider whose limit stands for a
  day is asked once an hour instead of once. An email an earlier version
  already put off for such a wait stays put off: it reads `scheduled`
  past its time, and `PATCH /emails/{id}` gives it a new one.
- When scheduled emails are put off because the provider's limit is
  reached, the log says each time that nothing that is due is sent, and
  for how long. It said that a send was refused, and not that
  everything waits.

## [0.1.1] - 2026-11-15

### Changed

- A send the mail provider refuses because the sending domain is not set
  up with it now answers 422 `sender_domain_not_ready`, where it answered
  422 `rejected_by_upstream` like one bad email. Cloudflare gives the same
  answer for both, so CairnPost reads the domain's DNS after such a
  refusal: when none of the records the provider asks is in place, the
  refusal is taken to be the domain's. The message names one of them and
  says how to check. A program that acts on
  `rejected_by_upstream` should handle the new name too, and treat it as
  the sender failing: no email from that domain goes out until it is set
  up. It is still a 422, and an email in a batch carries it the same way.
  A lookup that fails leaves the answer as it was, and so does a domain
  with any of those records in place.

## [0.1.0] - 2026-11-15

### Added

- `POST /emails`: send one email with an API key. A request with an
  `Idempotency-Key` repeated with the same content is one email.
- Several addresses: `to`, `cc` and `bcc` take one address or a list (50 in
  all, `CAIRNPOST_MAX_RECIPIENTS`). It is one email with one id, and
  `GET /emails/{id}` says under `recipients` what became of it at each
  address. A suppressed address among them is left out and named under
  `suppressed`, in the answer and in `GET /emails/{id}`; the email is
  refused when nobody in `to` or `cc` is left.
- `GET /emails/{id}`: what became of an email (`sending`, `sent`, `deferred`,
  `delivered`, `bounced`, `complained`, `failed`, `unknown`) and each event
  that happened to it.
- Attachments: `attachments` on `POST /emails` sends files with an email,
  beside it or shown in its HTML by a `content_id`, within
  `CAIRNPOST_MAX_ATTACHMENTS` and `CAIRNPOST_MAX_ATTACHMENT_BYTES`. What a
  file holds is never logged, and kept only under content retention. `cairnpost send -attach`. Only so many
  large sends are taken at one time (`CAIRNPOST_MAX_UPLOAD_BYTES`), and a
  request that does not arrive whole in time answers 408
  `request_incomplete`.
- Batches: `POST /emails/batch` sends a list of emails in one request
  (100, `CAIRNPOST_MAX_BATCH`). All are checked before any is sent; then
  each has its own id and outcome, answered in order, 200 when all were
  taken and 207 when some were not. With an `Idempotency-Key` the same
  batch sent again sends only what did not go.
- Sending later: `scheduled_at` on `POST /emails` (and on the emails of a
  batch) keeps an email to be sent at that time, up to
  `CAIRNPOST_SCHEDULE_AHEAD` ahead. `PATCH /emails/{id}` moves it and
  `POST /emails/{id}/cancel` cancels it; its state is `scheduled`, then
  `cancelled` or an ordinary email's. What it says and its files are held
  until it is sent or cancelled and deleted then. `cairnpost send -at`;
  a "Scheduled" part of the console's Emails page; `"scheduled"` in
  `GET /health`; `cairnpost_emails_scheduled`.
- Templates: `POST /templates` keeps an email written once, with
  variables as `{{name}}`, and `"template": {"name", "variables"}` on
  `POST /emails` (and on the emails of a batch) sends one from it. A value
  is escaped in the HTML. A template belongs to a sending domain or serves
  all (`CAIRNPOST_MAX_TEMPLATES` to each); `GET`, `PATCH` and `DELETE
  /templates/{id}`; a Templates page in the console; `cairnpost send
  -template -var`.
- Receiving email: with `CAIRNPOST_INBOUND_SECRET` set, the Worker in
  `worker/inbound` hands on the emails Cloudflare receives for your
  addresses, signed, to `POST /inbound`. Each is kept as it came for
  `CAIRNPOST_INBOUND_RETENTION` (7 days), told to endpoints as
  `email.received`, and read by an admin key: `GET /inbound`,
  `GET /inbound/{id}`, `.../raw`, `.../attachments/{place}`,
  `DELETE /inbound/{id}`. A "Received" part of the console's Emails page;
  `"inbound"` in `GET /health`; `cairnpost_inbound_emails`. An email is
  read in whatever charset it is written in.
- Lists that come in pages (`GET /suppressions`, `GET /inbound`,
  `GET /webhooks/{id}/deliveries`) share one rule: 50 to a page unless
  `?limit=` says otherwise, 200 at most, and `?after=` for the next.
- The `smtp` driver: send through any mail server that takes mail by SMTP
  (`CAIRNPOST_SMTP_*`), secured by STARTTLS or TLS, signed in by PLAIN or
  LOGIN. Such a server reports nothing after it took an email, so records
  stop at `sent`.
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
  An email's files are kept with it, for as long, and read back by
  `GET /emails/{id}/attachments/{place}` or from the console, unless
  `CAIRNPOST_CONTENT_FILES` is `false`.
- A console at `/console`, served by the same binary: sign in with an
  admin key to see whether each part of the server is working and to list,
  make and revoke API keys. `CAIRNPOST_CONSOLE`, `CAIRNPOST_CONSOLE_SESSION`
  and `CAIRNPOST_CONSOLE_PLAIN_HTTP` set it up.
- The console's first page shows how much was sent over the last 24
  hours, 7 days or 30 days and what became of it, as numbers and charts
  drawn by the server; each sending domain's bounce rate, marked at
  `CAIRNPOST_BOUNCE_WARN` percent (5); the mail provider's allowance; the
  webhook endpoints that are failing or paused, with how many webhooks
  each is owed; and whether each part of the server is working, which
  every page says at its top.
- The console says times in the time zone of the browser that asks, and
  cuts the first page's hours and days in it. Without scripts it is the
  server's own (`TZ`), or `CAIRNPOST_CONSOLE_TIMEZONE`.
- The console's pages are in parts, each with its own address: the list
  of a page first, its forms a tab away. A list is narrowed to a state by
  links above it.
- `GET /metrics`: the server's numbers for Prometheus, with an admin key:
  requests by route and status and how long they took, the last day's
  emails by state, webhook endpoints by how they stand, the provider's
  allowance, and whether each part of the server is working.
- `cairnpost tail` shows what becomes of emails as it is recorded.
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
- `openapi.json`: the whole API written down for programs (OpenAPI 3.1),
  for client generators and API browsers. A test holds its calls, keys,
  fields and listed values to the code.
- `cairnpost serve`, `migrate`, `send` and `version`; a Dockerfile and a
  Compose file with Postgres 18.
- Images for amd64 and arm64 at `ghcr.io/mettjs/cairnpost`, published with
  each release.

[Unreleased]: https://github.com/mettjs/CairnPost/compare/v0.1.2...HEAD
[0.1.2]: https://github.com/mettjs/CairnPost/compare/v0.1.1...v0.1.2
[0.1.1]: https://github.com/mettjs/CairnPost/compare/v0.1.0...v0.1.1
[0.1.0]: https://github.com/mettjs/CairnPost/releases/tag/v0.1.0
