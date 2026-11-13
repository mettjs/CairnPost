# Changelog

Every change a user of CairnPost can notice is written here. The format is
[Keep a Changelog](https://keepachangelog.com/en/1.1.0/) and versions follow
[Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

### Added

- `POST /emails`: send one email to one recipient with an API key. A request
  with an `Idempotency-Key` repeated with the same content is one email.
- `GET /emails/{id}`: what became of an email.
- `GET /health`.
- API keys with a scope (`send` or `admin`), optionally bound to one sending
  domain; stored hashed, shown once. `cairnpost keys create|list|revoke`.
- The `capture` driver, for development: it keeps emails in memory, sends
  nothing, and shows them at `GET /capture/emails` to an admin key.
- The `cloudflare` driver: delivery through Cloudflare Email Sending.
- `GET /limits`: the provider's sending allowance and how much is used.
- `cairnpost serve`, `migrate`, `send` and `version`; a Dockerfile and a
  Compose file with Postgres 18.

The first release will be v0.1.0.
