# CairnPost

[![CI](https://github.com/mettjs/CairnPost/actions/workflows/ci.yml/badge.svg)](https://github.com/mettjs/CairnPost/actions/workflows/ci.yml)
[![License: MIT](https://img.shields.io/badge/License-MIT-blue.svg)](LICENSE)

> A cairn marks the way. A post carries word along it.

A small, self-hostable transactional email service: one HTTP API, API keys
and idempotent sends, in front of the mail provider you choose. Written in
Go, backed by Postgres.

**Status: early.** It sends through Cloudflare
Email Sending and records that the provider took each email; delivery
events, webhooks and a suppression list come next, so it does not yet know
about a bounce. The [changelog](CHANGELOG.md) says what exists.

## What it is, and is not

- It is a gateway. It owns the API, the keys, the records and the tools; an
  upstream provider delivers the mail.
- **It never stores or logs an email's subject or body.** The database keeps
  who sent what to whom and what became of it, nothing more. The one
  exception is the `capture` driver, a development tool whose purpose is to
  show you the email: it holds the last few in memory only, readable with an
  admin key.
- It does not track opens or clicks, and it does not send newsletters.
- It comes with a small console to manage keys, domains and suppressions,
  served by the same binary. (Planned; not built yet.)
- It leans on Go's standard library. Its one direct dependency is the
  Postgres driver, [pgx](https://github.com/jackc/pgx).

## Try it

Released images are at `ghcr.io/mettjs/cairnpost`, for amd64 and arm64. To
run it from source with its database:

```sh
docker compose up -d --build
# Make a key. It is shown once.
export CAIRNPOST_API_KEY=$(docker compose exec -T cairnpost cairnpost keys create --name me --scope admin)

curl http://localhost:8025/emails \
  -H "Authorization: Bearer $CAIRNPOST_API_KEY" \
  -H "Idempotency-Key: welcome-42" \
  -d '{"from":"App <app@example.com>","to":"ana@example.org","subject":"Welcome","text":"Hello, Ana."}'
# {"id":"01a1d08e-34f8-7b25-bea5-12654e330b75"}

# With the capture driver, read what would have been sent:
curl http://localhost:8025/capture/emails -H "Authorization: Bearer $CAIRNPOST_API_KEY"
```

## API

Every call but `/health` needs `Authorization: Bearer <key>`.

| Call | Does |
|---|---|
| `POST /emails` | Send one email to one recipient. Answers `{"id": "..."}` once the provider has taken it. |
| `GET /emails/{id}` | What became of an email: `sending`, `sent`, `failed` or `unknown`. A key sees its own emails; an admin key sees any. |
| `GET /capture/emails` | The emails the capture driver holds, newest first. Admin key; only with `CAIRNPOST_DRIVER=capture`. |
| `GET /limits` | The provider's sending allowance and how much of it is used, so you know before it runs out. `quota` is `null` for a provider with none to tell. |
| `GET /health` | Whether the service and its database are up; the driver and version. |

`POST /emails` takes `from`, `to`, `subject`, and `html` or `text` or both;
`reply_to` and `headers` are optional. Any other field is refused, never
silently dropped.

**Idempotency.** Send an `Idempotency-Key` header and a request repeated
with the same key and the same content is one email: the repeat answers the
first one's id. A key is remembered for a day from the try that sent, and
belongs to the API key that used it.

**Errors** are JSON: `{"statusCode": 422, "name": "validation_error", "message": "..."}`.
The status tells a program what to do:

| Status | Name | Meaning |
|---|---|---|
| 422 | `validation_error`, `rejected_by_upstream` | The request is at fault and will never succeed. Do not retry. |
| 409 | `invalid_idempotent_request` | The key was already used with a different request, which was sent. Do not retry. |
| 409 | `concurrent_idempotent_requests` | A send under this key is in flight, or its answer has not come yet. Retry after `Retry-After`. |
| 401, 403 | `missing_api_key`, `invalid_api_key`, `domain_not_allowed`, `admin_key_required` | The key is missing, wrong, or not allowed to do this. |
| 413 | `request_too_large` | The body is over `CAIRNPOST_MAX_BODY_BYTES`. |
| 429 | `rate_limit_exceeded` | The provider's limit is reached; nothing was sent. Retry after `Retry-After`. |
| 502 | `upstream_unknown` | The provider did not answer; the email may have gone. Retry with the same key. This is the only answer that leaves it open. |
| 503 | `upstream_unavailable`, `unavailable` | Nothing was sent. Retry later. |

When a send fails after it was recorded (`rejected_by_upstream`, 429, 502,
503 `upstream_unavailable`), the error carries the email's `id`, and
`GET /emails/{id}` says what became of it.

## Drivers

**`cloudflare`** sends through [Cloudflare Email Sending](https://developers.cloudflare.com/email-service/).
You need a sending domain onboarded in the Cloudflare account, and an API
token with the *Email Sending* permission. Things worth knowing, each
measured against the live service:

- A new Cloudflare account may send 200 emails a day. `GET /limits` shows
  the allowance and what is used.
- A send answers as soon as Cloudflare has queued the email, typically in
  under a second. Whether it was delivered or bounced is told later, by
  events CairnPost does not read yet.
- Cloudflare has no idempotency of its own: the same request twice is two
  emails. CairnPost's `Idempotency-Key` is what makes it one.
- Cloudflare answers the same refusal for a bad address, a header it does
  not allow, and a sender domain the account has not onboarded. CairnPost
  passes each on as 422 `rejected_by_upstream`. If a send from a new domain
  is refused, check that the domain is onboarded.
- A wrong token or account id is never the email's fault: sends answer 503,
  nothing is dropped, and the server's log names Cloudflare's status.

Not measured: what Cloudflare answers once the day's allowance is spent.
So CairnPost does not depend on it: when a send is refused it asks
Cloudflare for the allowance, and if that is spent the send answers 429
with the time until it resets.

**`capture`** sends nothing. It keeps the last emails in memory and shows
them at `GET /capture/emails`: for development and tests.

## Command line

```
cairnpost serve                 run the HTTP API (applies migrations first)
cairnpost migrate               apply migrations and stop
cairnpost keys create           make an API key (shown once)
cairnpost keys list             list API keys
cairnpost keys revoke PREFIX    end an API key
cairnpost send                  send an email through a running server
cairnpost version               print the version
```

`keys` and `migrate` work on the database directly, so they run where the
server runs. A key has a scope, `send` or `admin`, and may be bound to one
sending domain with `--domain`.

## Configuration

| Variable | Default | Purpose |
|---|---|---|
| `CAIRNPOST_POSTGRES_DSN` | required | The database. Postgres 18 or later. |
| `CAIRNPOST_DRIVER` | required | Who delivers the mail: `cloudflare` or `capture`. See Drivers. |
| `CAIRNPOST_CLOUDFLARE_ACCOUNT_ID` | required for `cloudflare` | The Cloudflare account whose sending domains are used. |
| `CAIRNPOST_CLOUDFLARE_API_TOKEN` | required for `cloudflare` | An API token with the Email Sending permission. A secret. |
| `CAIRNPOST_ADDR` | `:8080` | Where the API listens. |
| `CAIRNPOST_IDEMPOTENCY_TTL` | `24h` | How long an `Idempotency-Key` is remembered. |
| `CAIRNPOST_IN_DOUBT_WINDOW` | `2m` | How long a send whose answer was lost blocks a retry of its key. After it, the retry is sent: an email that arrives twice is better than one that never does. Must be longer than the send timeout and shorter than the key's lifetime. |
| `CAIRNPOST_SEND_TIMEOUT` | `10s` | The most one call to the provider may take. |
| `CAIRNPOST_MAX_BODY_BYTES` | `1048576` | The largest request body accepted. |
| `CAIRNPOST_CAPTURE_KEEP` | `100` | How many emails the capture driver holds. |

`cairnpost send` reads `CAIRNPOST_URL` and `CAIRNPOST_API_KEY`.

## Contributing

Commits, versions and releases follow [CONTRIBUTING.md](CONTRIBUTING.md).

## License

[MIT](LICENSE)
