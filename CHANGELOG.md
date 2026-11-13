# Changelog

Every change a user of CairnPost can notice is written here. The format is
[Keep a Changelog](https://keepachangelog.com/en/1.1.0/) and versions follow
[Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

## [0.1.0] - 2026-11-12

The first release: CairnPost takes an email over HTTP with an API key and
hands it to Cloudflare Email Sending, once. It does not yet read delivery
events, so it does not know about a bounce.

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
- Released images for amd64 and arm64 at `ghcr.io/mettjs/cairnpost`.

[Unreleased]: https://github.com/mettjs/CairnPost/compare/v0.1.0...HEAD
[0.1.0]: https://github.com/mettjs/CairnPost/releases/tag/v0.1.0
