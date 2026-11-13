# CairnPost

[![CI](https://github.com/mettjs/CairnPost/actions/workflows/ci.yml/badge.svg)](https://github.com/mettjs/CairnPost/actions/workflows/ci.yml)
[![License: MIT](https://img.shields.io/badge/License-MIT-blue.svg)](LICENSE)

> A cairn marks the way. A post carries word along it.

A small, self-hostable transactional email service: one HTTP API, API keys
and idempotent sends, in front of the mail provider you choose. Written in
Go, backed by Postgres.

**Status: early.** It sends through Cloudflare Email Sending, reads what
became of each email, and stops sending to addresses that bounce for good
or complain, tells your application by signed webhooks, and deletes old
records on a schedule. A console comes next. The [changelog](CHANGELOG.md) says what
exists.

## What it is, and is not

- It is a gateway. It owns the API, the keys, the records and the tools; an
  upstream provider delivers the mail.
- **By default it keeps nothing of an email's subject or body, and it never
  writes them to a log.** The database holds who sent what to whom and what
  became of it. Whoever runs it may turn on `CAIRNPOST_CONTENT_RETENTION` to
  keep what emails said for a short time, to see what went out when
  something goes wrong (see "What is kept").
  The `capture` driver, a development tool whose purpose is to show you the
  email, holds the last few in memory only, readable with an admin key.
- It does not track opens or clicks.
- It is for transactional email: it does not send newsletters or manage
  audiences.
- It comes with a small console, served by the same binary at `/console`:
  sign in with an admin key to see how the server is doing and manage its
  API keys. More pages are being added.
- It leans on Go's standard library. Its one direct dependency is the
  Postgres driver, [pgx](https://github.com/jackc/pgx).

## Try it

Each release publishes an image at `ghcr.io/mettjs/cairnpost`, for amd64 and
arm64; none is released yet. To run it from source with its database:

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
| `GET /emails/{id}` | What became of an email, and each event that happened to it. A key sees its own emails; an admin key sees any, or its sending domain's if it is bound to one. Where content is kept, an admin key also sees what the email said, as `content`. |
| `GET /capture/emails` | The emails the capture driver holds, newest first. Admin key; only with `CAIRNPOST_DRIVER=capture`. |
| `GET /suppressions` | The addresses not sent to, in order of domain and address. Admin key. `?domain=` narrows to one sending domain. A page is 100 unless `?limit=` says otherwise (1000 at most); when more follow, the answer has a `next` to pass as `?after=`. |
| `POST /suppressions` | Stop sending to an address from a domain: `{"domain", "address", "note"}`. The note is optional, one line of at most 500 bytes. Admin key. |
| `POST /suppressions/lift` | Send to an address again: `{"domain", "address"}`. Or to every suppressed address of a domain: `{"domain", "all": true}`, which `"reason"` narrows to `bounce`, `complaint` or `manual`. Answers `{"lifted": n}`. Admin key. |
| `POST /webhooks` | Have a URL told what becomes of emails: `{"url", "domain", "events", "description"}`; all but `url` optional. Answers the endpoint with its signing `secret`, which is said only here. Admin key. |
| `GET /webhooks`, `GET /webhooks/{id}` | The endpoints. Admin key. |
| `PATCH /webhooks/{id}` | Change any of `url`, `events`, `description`, `enabled`. Admin key. |
| `DELETE /webhooks/{id}` | Remove an endpoint and all it was owed. Admin key. |
| `GET /webhooks/{id}/deliveries` | What the endpoint was sent and how each went, newest first. `?state=` narrows to `pending`, `delivered` or `failed`. A page is 50 unless `?limit=` says otherwise (500 at most); when more follow, the answer has a `next` to pass as `?after=`. Admin key. |
| `POST /webhooks/{id}/deliveries/{delivery}/retry` | Try a webhook now: once more if it was given up, or without waiting for its next try if it is pending. Admin key. |
| `POST /webhooks/{id}/test` | Send the endpoint a signed `webhook.test` now and say how it answered. Admin key. |
| `GET /limits` | The provider's sending allowance and how much of it is used, so you know before it runs out. `quota` is `null` for a provider with none to tell. |
| `GET /health` | Whether the service and its database are up; the driver, the version, and whether delivery events are being read, webhooks sent and old records pruned. |

`POST /emails` takes `from`, `to`, `subject`, and `html` or `text` or both;
`reply_to` and `headers` are optional. Any other field is refused, never
silently dropped.

**An email's state**, from `GET /emails/{id}`:

| State | Meaning |
|---|---|
| `sending` | Handed to the provider; its answer is not in yet. |
| `sent` | The provider took it. Nothing more is known yet. |
| `deferred` | The receiving server said "not now"; the provider is retrying. |
| `delivered` | The receiving server took it. |
| `bounced` | The receiving server refused it. The event says whether for good (`permanent`). |
| `complained` | It arrived, and its recipient marked it as spam. |
| `failed` | It was not sent: the provider refused it, or took it and then could not or would not send it. |
| `unknown` | The provider never answered the send, so the email may or may not have gone. |

States past `sent` come from the provider's delivery events, which need the
driver's event settings (see Drivers). Each event is listed under `events`
with its `type`, its time and, when there is one, the receiving server's
status `code` ("5.1.1") and the provider's short `reason`
("permanent_failure"). The receiving server's own sentence can quote the
email, so it is kept only where content is (see "What an email said").

**Suppression.** When a receiving server refuses an address for good (a hard
bounce), or its owner marks an email as spam, CairnPost stops sending to
that address from that sending domain: writing there again only harms the
domain's standing with mail providers. A send to it answers 422
`recipient_suppressed` and nothing is sent. Nothing else suppresses: not a
full mailbox, not a delay, and not a refusal that is about the sender (a
status of class 5.7: a failed signature, a policy, a blocklist), or one
fault in the domain's setup would shut out everyone written to while it
lasted. Each sending domain has its own list. The list is read when a send
starts: an email already on its way when the bounce arrives still goes.

A suppression is not always right. A mailbox can be made again, or a person
can mark a wanted email as spam by mistake, and they are then shut out of
their own sign-in codes. An admin lifts one with `POST /suppressions/lift`
or `cairnpost suppressions remove`, and a whole domain's with `"all": true`
or `-all`. A lift is remembered: a late bounce of an email sent before it
does not bring the suppression back, but an email sent after it that
bounces for good does. An admin key bound to a sending domain manages that
domain's list only. This needs delivery events to be read (see Drivers).

**Webhooks.** An endpoint is a URL of yours that CairnPost posts to when
something becomes of an email, so you need not ask. It hears about one
sending domain or all of them, and the event types you name or all of them:

| Type | When |
|---|---|
| `email.delivered` | The receiving server took the email. |
| `email.delivery_delayed` | It said "not now"; the provider keeps trying. |
| `email.bounced` | It refused the email. `data.bounce.type` says how. |
| `email.complained` | The recipient marked the email as spam. |
| `email.failed` | The provider took the email and then did not send it. |

```json
{
  "type": "email.bounced",
  "created_at": "2026-01-02T03:04:05Z",
  "data": {
    "email_id": "0199...",
    "from": "app@mail.example.com",
    "to": ["ana@example.org"],
    "created_at": "2026-01-02T03:04:01Z",
    "code": "5.1.1",
    "reason": "permanent_failure",
    "bounce": {"type": "Permanent"},
    "suppressed": true
  }
}
```

A webhook never carries the email's subject or body. `bounce.type` is
`Permanent` when the address itself will never take mail, `Transient` when
it may yet, and `Policy` when the receiving server refused for good for a
reason that is the sender's (a failed signature, a blocklist): nothing is
wrong with that address, so do not ask its owner to change it.
`suppressed` says that, with this event, the address is on the sending
domain's suppression list. A `Permanent` bounce can come with `suppressed`
false: when the suppression was lifted and the email that bounced was sent
before the lift.

Each webhook is signed as [Standard Webhooks](https://www.standardwebhooks.com/)
describes, which is also how Svix signs, so a library for either verifies
it with the endpoint's `secret`. The headers go out under both names:
`webhook-id`, `webhook-timestamp`, `webhook-signature` and `svix-id`,
`svix-timestamp`, `svix-signature`. The signature is the HMAC-SHA256 of
`id.timestamp.body` under the key inside the secret. Verify it before
believing a webhook, and refuse a timestamp far from now. The id is the
same on every try of one webhook, so use it to tell a repeat: a webhook can
arrive more than once, and webhooks can arrive out of order.

Answer with any 2xx within `CAIRNPOST_WEBHOOK_TIMEOUT`. Anything else, a
redirect included, is a failed try: the webhook is tried again after each
wait in `CAIRNPOST_WEBHOOK_RETRIES` (about 28 hours in all by default) and
then given up, after which `…/retry` tries it once more. Webhooks go out
`CAIRNPOST_WEBHOOK_PER_ENDPOINT` at a time to one endpoint, so one that is
slow does not hold up another's. A disabled
endpoint is owed nothing new, and what it was owed waits until it is
enabled again, or until its email's record is pruned. An endpoint must be `https://` at a public address, unless
the operator sets `CAIRNPOST_WEBHOOK_ALLOW_PRIVATE` for an application on
the same private network. An admin key bound to a sending domain manages
that domain's endpoints only. Webhooks need delivery events to be read (see
Drivers); a bounce of an email CairnPost has no record of suppresses the
address but sends no webhook, so treat a 422 `recipient_suppressed` as the
same news.

**Idempotency.** Send an `Idempotency-Key` header and a request repeated
with the same key and the same content is one email: the repeat answers the
first one's id. A key is remembered for a day from the try that sent, and
belongs to the API key that used it.

**What is kept, and for how long.** An email's record (who sent it, to
whom, what became of it, its events and the webhooks sent for them) is kept
for `CAIRNPOST_RETENTION`, 30 days by default, counted from the last thing
that happened to it (the send, or its latest event), and then deleted: after
that `GET /emails/{id}` answers 404 and its webhooks are no longer listed or
retried. An `Idempotency-Key` is deleted when its day is over. A suppression
in force is never deleted: it outlives the email that caused it, and then
names no email. A suppression that was lifted is forgotten once the lift is
that old too and every email sent before it is gone, so until then a late
bounce cannot undo the lift. The retention is 7 days at least, because a receiving server may go on
trying an email for days before it bounces; `off` keeps records for ever.
The pruner runs every `CAIRNPOST_PRUNE_EVERY`, and `GET /health` says
`"pruning": "running"` or `"failing"`. Mail still goes while it is failing.

**What an email said** is not kept unless `CAIRNPOST_CONTENT_RETENTION` is
set, to a time of 30 days at most. While it is, the subject, bodies, reply
address, sender's name and headers of each email are stored when it is sent,
and `GET /emails/{id}` shows them as `content`, with `kept_until`, to an
admin key: one bound to a sending domain sees that domain's emails' only,
and a key that may only send never does, even for its own emails. So is
what the receiving server said about a bounce or a delay, as an event's
`detail`: it says why, and it can quote the email, so it is kept until the
email's content is and not at all if it comes later. Neither is ever logged
or sent in a webhook. Both stop being shown when their time is up and are
deleted at the pruner's next pass. Each is deleted on the time it was kept
under: turning the setting off, or making it shorter, hides at once what
is past the new time, and deletes it when its original time is up. Anyone
who can read the database can read them, so leave it off unless you need
it.

**Errors** are JSON: `{"statusCode": 422, "name": "validation_error", "message": "..."}`.
The status tells a program what to do:

| Status | Name | Meaning |
|---|---|---|
| 422 | `validation_error`, `rejected_by_upstream` | The request is at fault and will never succeed. Do not retry. |
| 422 | `recipient_suppressed` | The address is on the sending domain's suppression list. Nothing was sent now. Do not retry; lift the suppression if it is wrong. If the answer has an `id`, an earlier try under the same `Idempotency-Key` got no answer from the provider and may have gone: the id is that try's. |
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
  under a second. Whether it was delivered or bounced is told a second or
  more later, by an event.
- **To read events**, create a Queue in the Cloudflare account, add an Email
  Sending event subscription for your sending domain that publishes to it,
  enable an HTTP pull consumer on it, give the API token the *Queues*
  permission too, and set `CAIRNPOST_CLOUDFLARE_QUEUE_ID`. Without it an
  email's record stops at `sent`. Several CairnPost instances may read one
  queue; an event is recorded once.
- **Raise the consumer's "max retries".** A Cloudflare queue hands a message
  over a limited number of times (3 unless you change it) and then deletes
  it. CairnPost takes no events while its database is down and hands back
  what it could not record, but each hand-back uses one of those tries. Set
  it high, and consider a dead-letter queue, so an outage loses no bounce.
- `GET /health` says `"events": "reading"`, `"failing"` or `"off"`. Mail
  still goes while events are failing, so the status stays `ok`; watch the
  field.
- CairnPost should be the only reader of that queue: an event another
  reader acknowledges never reaches it.
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

## Console

The console is at `/console` on the same address as the API. Sign in with
an admin key; a key that may only send is refused. It shows whether each
part of the server is working, lists the emails on record and what became
of each, manages the suppression list and the webhook endpoints, and
lists, makes and revokes API keys. A new key is shown once,
on the page that made it.

- A sign-in lasts `CAIRNPOST_CONSOLE_SESSION` (12 hours), ends when you
  sign out, and ends at once if the key is revoked. Make the setting
  shorter and the sign-ins older than it end too.
- An admin key bound to a sending domain sees, makes and revokes that
  domain's keys only, and sees that domain's emails, suppressions and
  webhook endpoints only.
- Emails are listed newest first, a hundred to a page, and can be narrowed
  to a state and to one recipient. The address you look for is kept with
  your session on the server, not in the page's address, so it is in no
  proxy's log and not in the browser's history, and it is forgotten when
  the session ends. The list never shows a subject.
- The suppression list shows who is not sent to and why, with the email
  it happened to: the first 500 in order of sending domain and address,
  narrowed to a domain or an address (`cairnpost suppressions list` on
  the server shows them all). An address is suppressed by hand or sent to again from
  there, and a sending domain's suppressions can be lifted together; both
  ask first. Looking for an address narrows the emails and the
  suppressions alike, so one search answers "was it sent, did it arrive,
  is it blocked".
- A webhook endpoint is made, changed, turned off and on, tested and
  removed there. Its signing secret is shown once, on the page that made
  it. "Send a test" posts a signed `webhook.test` and says how the
  endpoint answered. Its page lists what it was sent, fifty to a page,
  and tries again what was not delivered.
- An email's page shows what happened to it and, where
  `CAIRNPOST_CONTENT_RETENTION` keeps it, what it said: as the text it was
  written in, never drawn as a mail program would.
- A key made in the console says which key made it, and the server's log
  says who signed in, made a key or revoked one, by the keys' public
  prefixes. If a key leaks, the keys it made can be found.
- A sending domain may have `CAIRNPOST_MAX_KEYS` live keys (200), and so
  may the keys bound to no domain. Revoking one makes room.
- The session cookie is marked `Secure`, so the console needs https. To
  open it over plain http on your own machine, set
  `CAIRNPOST_CONSOLE_PLAIN_HTTP=on`.
- A change asked for from another site is refused, pages are never cached
  or framed, and no page runs a script that is not the console's own.
- `CAIRNPOST_CONSOLE=off` leaves the console out; the API is unchanged.

## Command line

```
cairnpost serve                 run the HTTP API (applies migrations first)
cairnpost migrate               apply migrations and stop
cairnpost keys create           make an API key (shown once)
cairnpost keys list             list API keys
cairnpost keys revoke PREFIX    end an API key
cairnpost suppressions list     list the addresses not sent to
cairnpost suppressions add      stop sending to an address
cairnpost suppressions remove   send to an address again
cairnpost webhooks create       have a URL told what becomes of emails
cairnpost webhooks list         list the webhook endpoints
cairnpost webhooks remove ID    remove a webhook endpoint
cairnpost send                  send an email through a running server
cairnpost version               print the version
```

`keys`, `suppressions`, `webhooks` and `migrate` work on the database directly, so they run where the
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
| `CAIRNPOST_CLOUDFLARE_QUEUE_ID` | none | The Queue Cloudflare publishes delivery events to. Without it, events are not read. |
| `CAIRNPOST_EVENTS_POLL` | `5s` | How long the reader of events rests after finding none. |
| `CAIRNPOST_EVENTS_GRACE` | `2m` | How long an event about an email with no record waits for that record before it is let go. |
| `CAIRNPOST_EVENTS_BATCH` | `50` | How many events are taken at once, 100 at most. |
| `CAIRNPOST_EVENTS_LEASE` | `1m` | How long a batch taken stays this reader's, and so how long it has to record it. |
| `CAIRNPOST_EVENTS_RETRY` | `30s` | How long an event handed back waits before it is given again. |
| `CAIRNPOST_IDEMPOTENCY_TTL` | `24h` | How long an `Idempotency-Key` is remembered. |
| `CAIRNPOST_RETENTION` | `30d` | How long an email's record is kept after the last thing that happened to it, in days (`30d`) or hours (`720h`). At least `7d`, and longer than `CAIRNPOST_IDEMPOTENCY_TTL` and than the waits of `CAIRNPOST_WEBHOOK_RETRIES` together. `off` keeps records for ever. |
| `CAIRNPOST_CONTENT_RETENTION` | `off` | How long what an email said, and what the receiving server said of it, is kept and shown to admin keys: a time such as `24h` or `7d`, `30d` at most and no longer than `CAIRNPOST_RETENTION`. `off` keeps none. |
| `CAIRNPOST_PRUNE_EVERY` | `1h` | How long the pruner rests between passes. |
| `CAIRNPOST_IN_DOUBT_WINDOW` | `2m` | How long a send whose answer was lost blocks a retry of its key. After it, the retry is sent: an email that arrives twice is better than one that never does. Must be longer than the send timeout and shorter than the key's lifetime. |
| `CAIRNPOST_SEND_TIMEOUT` | `10s` | The most one call to the provider may take. |
| `CAIRNPOST_MAX_BODY_BYTES` | `1048576` | The largest request body accepted. |
| `CAIRNPOST_CAPTURE_KEEP` | `100` | How many emails the capture driver holds. |
| `CAIRNPOST_CONSOLE` | `on` | Serve the console at `/console`. `off` leaves it out. |
| `CAIRNPOST_CONSOLE_SESSION` | `12h` | How long a sign-in to the console lasts: a minute at least, 30 days at most. |
| `CAIRNPOST_CONSOLE_PLAIN_HTTP` | `off` | Let the console's session cookie travel without TLS, for a console opened over plain http in development. |
| `CAIRNPOST_MAX_KEYS` | `200` | How many keys that are not revoked one sending domain may have, and how many the keys bound to no domain may be. |
| `CAIRNPOST_WEBHOOK_TIMEOUT` | `10s` | The most one try at delivering a webhook may take. |
| `CAIRNPOST_WEBHOOK_RETRIES` | `5s,5m,30m,2h,5h,10h,10h` | The waits before each further try. A webhook is tried once, then once after each wait, then given up. |
| `CAIRNPOST_WEBHOOK_WORKERS` | `8` | How many webhooks are being sent at once, to all endpoints together. |
| `CAIRNPOST_WEBHOOK_PER_ENDPOINT` | `2` | How many are being sent at once to one endpoint. Under the number of workers, it keeps an endpoint that hangs from holding up the others. |
| `CAIRNPOST_WEBHOOK_MAX_ENDPOINTS` | `20` | How many endpoints may hear about one sending domain, and how many about all. |
| `CAIRNPOST_WEBHOOK_POLL` | `2s` | How long the sender rests when nothing is due. |
| `CAIRNPOST_WEBHOOK_ALLOW_PRIVATE` | `off` | Let an endpoint be plain `http://` and at an address that is not on the public internet: for an application on the same private network. |

`cairnpost send` reads `CAIRNPOST_URL` and `CAIRNPOST_API_KEY`.

## Contributing

Commits, versions and releases follow [CONTRIBUTING.md](CONTRIBUTING.md).

## License

[MIT](LICENSE)
