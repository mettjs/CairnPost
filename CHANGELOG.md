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
- `cairnpost serve`, `migrate`, `send` and `version`; a Dockerfile and a
  Compose file with Postgres 18.
- Images for amd64 and arm64 at `ghcr.io/mettjs/cairnpost`, published with
  each release.
