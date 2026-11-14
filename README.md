<p align="center">
  <picture>
    <source media="(prefers-color-scheme: dark)" srcset="docs/logo-dark.svg">
    <img src="docs/logo-light.svg" alt="CairnPost" width="330" height="72">
  </picture>
</p>

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
records on a schedule, and comes with a console to watch and manage it.
The [changelog](CHANGELOG.md) says what exists.

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
  sign in with an admin key to see how much was sent and what became of
  it, and to manage emails, suppressions, webhooks, domains and API keys.
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
| `POST /emails` | Send one email, to one address or several. Answers `{"id": "..."}` once the provider has taken it, with `suppressed` naming any address it was not sent to. |
| `POST /emails/batch` | Send several emails in one request. Answers what became of each, in their order. |
| `GET /emails/{id}` | What became of an email, and each event that happened to it. A key sees its own emails; an admin key sees any, or its sending domain's if it is bound to one. Where content is kept, an admin key also sees what the email said, as `content`. |
| `GET /capture/emails` | The emails the capture driver holds, newest first. Admin key; only with `CAIRNPOST_DRIVER=capture`. |
| `GET /suppressions` | The addresses not sent to, in order of domain and address. Admin key. `?domain=` narrows to one sending domain. A page is 100 unless `?limit=` says otherwise (1000 at most); when more follow, the answer has a `next` to pass as `?after=`. |
| `POST /suppressions` | Stop sending to an address from a domain: `{"domain", "address", "note"}`. The note is optional, one line of at most 500 bytes. Admin key. |
| `POST /suppressions/lift` | Send to an address again: `{"domain", "address"}`. Or to every suppressed address of a domain: `{"domain", "all": true}`, which `"reason"` narrows to `bounce`, `complaint` or `manual`. Answers `{"lifted": n}`. Admin key. |
| `POST /webhooks` | Have a URL told what becomes of emails: `{"url", "domain", "events", "description"}`; all but `url` optional. Answers the endpoint with its signing `secret`, which is said only here. Admin key. |
| `GET /webhooks`, `GET /webhooks/{id}` | The endpoints. Admin key. |
| `PATCH /webhooks/{id}` | Change any of `url`, `events`, `description`, `enabled`. Admin key. |
| `DELETE /webhooks/{id}` | Remove an endpoint and all it was owed. Admin key. |
| `GET /webhooks/{id}/deliveries` | What the endpoint was sent and how each went, newest first. `?state=` narrows to `pending`, `delivered` or `failed`. A page is 50 unless `?limit=` says otherwise (500 at most); when more follow, the answer has a `next` to pass as `?after=`. Admin key. Each names its email in `email_id`, absent for an `address.suppressed`, which is about no email. |
| `POST /webhooks/{id}/deliveries/{delivery}/retry` | Try a webhook now: once more if it was given up, or without waiting for its next try if it is pending. Admin key. |
| `POST /webhooks/{id}/test` | Send the endpoint a signed `webhook.test` now and say how it answered. Admin key. |
| `POST /webhooks/{id}/secret` | Give the endpoint a new signing secret. Answers the endpoint with the new `secret`, said this once, and `previous_secret_until`: until then webhooks are signed with the old one too. Admin key. |
| `POST /webhooks/{id}/resume` | End an endpoint's pause, and send what it is owed. One that is not paused is answered as it is. Admin key. |
| `GET /limits` | The provider's sending allowance and how much of it is used, so you know before it runs out. `quota` is `null` for a provider with none to tell. |
| `GET /metrics` | The server's numbers in the text form Prometheus reads. Admin key. |
| `GET /health` | Whether the service and its database are up; the driver, the version, and whether delivery events are being read, webhooks sent and old records pruned. |

`POST /emails` takes `from`, `to`, `subject`, and `html` or `text` or both;
`cc`, `bcc`, `reply_to`, `headers` and `attachments` are optional. Any
other field is refused, never silently dropped.

**Attachments.** `attachments` is a list of files, each
`{"filename", "content", "content_type", "content_id"}`: `content` is the
file's bytes in base64. `content_type` is a media type with no parameter
but a charset; left out, it is told from the filename's ending, and is
`application/octet-stream` for one CairnPost does not know. A filename is
a name, not a path, with no quote and no character that cannot be seen.
Give a file a `content_id` and the HTML body can show it
(`<img src="cid:logo">` for `"content_id": "logo"`; the HTML must name
it); without one it is a file beside the email. An email carries 10 files and 10 MiB of them in
all unless `CAIRNPOST_MAX_ATTACHMENTS` and `CAIRNPOST_MAX_ATTACHMENT_BYTES`
say otherwise, and a send's body may be that much larger than any other
request's. A file is part of the request an `Idempotency-Key` remembers.
**What a file holds is never kept and never logged**: it is handed to the
mail provider and let go. Its name, type and size are kept only where
what emails say is. Large files take time to hand on: a server that sends
them wants a `CAIRNPOST_SEND_TIMEOUT` to match, and a request must arrive
whole within 30 seconds, or it answers 408 `request_incomplete`. A server
holds a send's files in memory while it sends, so it takes only so many
large sends at one time (`CAIRNPOST_MAX_UPLOAD_BYTES`) and answers the
next 503 `unavailable` with `Retry-After`.

**Several addresses.** `to`, `cc` and `bcc` each take one address or a list
of them: 50 in all unless `CAIRNPOST_MAX_RECIPIENTS` says otherwise, one
`to` at least, no address twice. It is one email with one id, and each
address has an outcome of its own: `GET /emails/{id}` lists them under
`recipients`, each with its `kind` (`to`, `cc`, `bcc`) and its `state`.
The email's own `to` and `state` are those of its first address, so read
`recipients` to know what became of an email to several: `failed` there
says it did not go to that address, not that it went to nobody. An event
says which address it is about in `to`, and a webhook does the same. An
`Idempotency-Key` is freed by a failure only once the email failed at
every address.

**Several emails in one request.** `POST /emails/batch` takes a JSON list
of emails, each as `POST /emails` takes it: 100 at most unless
`CAIRNPOST_MAX_BATCH` says otherwise, and the whole request no larger than
one email with its files may be.

```sh
curl http://localhost:8025/emails/batch \
  -H "Authorization: Bearer $KEY" -H "Idempotency-Key: welcome-run-42" \
  -d '[{"from":"App <app@example.com>","to":"ana@example.org","subject":"Welcome","text":"Hello Ana"},
       {"from":"App <app@example.com>","to":"ben@example.org","subject":"Welcome","text":"Hello Ben"}]'
# {"data":[{"id":"01a1d08e-..."},{"id":"01a1d08f-..."}]}
```

Every email is checked before any is sent. One that could never be sent
(a bad address, a domain the key may not send from) refuses the whole
batch with the error it would have had alone, naming it by its place
("Email 2 of the batch: ..."), and nothing is sent. After that each email
stands alone: it has its own id and record, and one failing does not stop
the others. The answer lists them in the order they were given, each with
its `id` (and `suppressed`, if addresses were left out) or with the
`error` it would have answered alone.

| Status | Meaning |
|---|---|
| 200 | The mail provider took every email. |
| 207 | It did not take some, or any: read `data`. Each `error` has the `statusCode` and `name` of the same failure on `POST /emails`, and says the same about trying again. |
| 4xx, 503 | The batch was refused whole. Nothing was sent. |

Two errors are a batch's own. Once the mail provider says its sending
limit is reached, the emails not yet tried are not tried and answer
`rate_limit_exceeded` too. And a batch starts emails for a minute
(`CAIRNPOST_BATCH_TIMEOUT`), a few at one time
(`CAIRNPOST_BATCH_PARALLEL`): an email whose turn did not come, then or
because the server was stopping, answers 503 `not_started`. Nothing of it
was sent.

With an `Idempotency-Key` a batch is safe to send again as it was: each
email is remembered under the key and its place, so the second try answers
the ids of what went and sends only what did not. Send the same emails in
the same order: a place that holds another email answers
`invalid_idempotent_request`.

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
| `unknown` | The provider never answered the send, so the email may or may not have gone. A send left `sending` by a server that stopped in the middle of it is set to this within a few minutes (see `CAIRNPOST_IN_DOUBT_WINDOW`). |

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
`recipient_suppressed` and nothing is sent. When an email names several
addresses and some are suppressed, it goes to the others, and the answer
lists the ones left out under `suppressed`, as `GET /emails/{id}` and a
repeat under the same `Idempotency-Key` do afterwards. Left with no `to`,
its first `cc` is sent as `to`. It is refused when nobody in `to` or `cc`
is left: a `bcc` is never sent as `to`, where the other hidden addresses
would see it. Nothing else suppresses: not a
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
| `address.suppressed` | An address was put on the suppression list because of a bounce or a complaint about an email CairnPost has no record of. |

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

An email CairnPost has no record of (its send's answer was lost, or its
record was already deleted) has no event to tell of. If its bounce or
complaint suppresses the address, that is told on its own, once:

```json
{
  "type": "address.suppressed",
  "created_at": "2026-01-02T03:04:05Z",
  "data": {"domain": "mail.example.com", "address": "ana@example.org", "reason": "bounce"}
}
```

An email on record never sends this: its own webhook says `suppressed`.
Nor does a suppression added by hand.

Each webhook is signed as [Standard Webhooks](https://www.standardwebhooks.com/)
describes, which is also how Svix signs, so a library for either verifies
it with the endpoint's `secret`. The headers go out under both names:
`webhook-id`, `webhook-timestamp`, `webhook-signature` and `svix-id`,
`svix-timestamp`, `svix-signature`. The signature is the HMAC-SHA256 of
`id.timestamp.body` under the key inside the secret. Verify it before
believing a webhook, and refuse a timestamp far from now. The id is the
same on every try of one webhook, so use it to tell a repeat: a webhook can
arrive more than once, and webhooks can arrive out of order.

To change an endpoint's secret, `POST /webhooks/{id}/secret`. For
`CAIRNPOST_WEBHOOK_SECRET_OVERLAP` after (24 hours by default), each
webhook carries more than one signature in the one header, separated by a
space: one under the new secret and one under the old. A verifier accepts
a webhook when any signature matches, so the application can change over
at any moment in that time without refusing one. Each old secret signs
for its own time, so changing the secret twice does not cut off an
application that still holds the first; the last four changed sign at
most.

Answer with any 2xx within `CAIRNPOST_WEBHOOK_TIMEOUT`. Anything else, a
redirect included, is a failed try: the webhook is tried again after each
wait in `CAIRNPOST_WEBHOOK_RETRIES` (about 28 hours in all by default) and
then given up, after which `…/retry` tries it once more. Webhooks go out
`CAIRNPOST_WEBHOOK_PER_ENDPOINT` at a time to one endpoint, so one that is
slow does not hold up another's. A disabled
endpoint is owed nothing new, and what it was owed waits until it is
enabled again, or until its email's record is pruned.

While every try at an endpoint fails, `failing_since` says since when; one
answer ends it, to a webhook or to a test. When a webhook is given up and
the failing has gone on for `CAIRNPOST_WEBHOOK_PAUSE_AFTER` (5 days by
default), the endpoint is paused: `paused_at` is set, and nothing more is
sent to it. Unlike a disabled one it is still owed everything that
happens. `POST /webhooks/{id}/resume` resumes it: what it is owed is sent
then, with what was given up while it was failing. Changing an endpoint,
`"enabled": true` included, does not resume it.
 An endpoint must be `https://` at a public address, unless
the operator sets `CAIRNPOST_WEBHOOK_ALLOW_PRIVATE` for an application on
the same private network. An admin key bound to a sending domain manages
that domain's endpoints only. Webhooks need delivery events to be read (see
Drivers).

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
How many emails were sent each hour from each sending domain, and how many
of them were delivered, bounced and so on, is counted apart from the
records and kept for `CAIRNPOST_COUNTS_RETENTION`, 62 days by default,
whatever the records' retention is: numbers only, with no address in them.
The pruner runs every `CAIRNPOST_PRUNE_EVERY`, and `GET /health` says
`"pruning": "running"` or `"failing"`. Mail still goes while it is failing.

**What an email said** is not kept unless `CAIRNPOST_CONTENT_RETENTION` is
set, to a time of 30 days at most. While it is, the subject, bodies, reply
address, sender's name and headers of each email, and the name, type and
size of each file with it, are stored when it is sent,
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
| 422 | `recipient_suppressed` | The address is on the sending domain's suppression list, or every address in `to` and `cc` is when the email names several. Nothing was sent now. Do not retry; lift the suppression if it is wrong. If the answer has an `id`, an earlier try under the same `Idempotency-Key` got no answer from the provider and may have gone: the id is that try's. |
| 409 | `invalid_idempotent_request` | The key was already used with a different request, which was sent. Do not retry. |
| 409 | `concurrent_idempotent_requests` | A send under this key is in flight, or its answer has not come yet. Retry after `Retry-After`. |
| 401, 403 | `missing_api_key`, `invalid_api_key`, `domain_not_allowed`, `admin_key_required` | The key is missing, wrong, or not allowed to do this. |
| 404 | `not_found` | There is no such email, endpoint or suppression, or it is not this key's to see. |
| 404 | `route_not_found` | There is nothing at this path: the path or the server's address is wrong. |
| 405 | `method_not_allowed` | The path is there, and does not take this method. `Allow` names the ones it takes. |
| 408 | `request_incomplete` | The request did not arrive whole in time. Nothing was done; try again. |
| 413 | `request_too_large` | The body is over `CAIRNPOST_MAX_BODY_BYTES`, or a send's is over that and its files' allowance. |
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

Not measured, but read from Cloudflare's documentation: sending files. It
says a whole email may be 25 MiB, files included and counted as they
travel, in base64, a third larger than the files are: keep
`CAIRNPOST_MAX_ATTACHMENT_BYTES` under about 18 MiB. A file with a
`content_id` is refused before anything is sent (422 `validation_error`):
how the send call takes a file the HTML shows is not documented, and a
wrong guess would send emails with their pictures missing.

Not measured either: what Cloudflare answers once the day's allowance is spent.
So CairnPost does not depend on it: when a send is refused it asks
Cloudflare for the allowance, and if that is spent the send answers 429
with the time until it resets.

**`smtp`** sends through any mail server that takes mail by SMTP: your
own, or a provider's relay. It is the way to use a provider CairnPost has
no driver for. Things worth knowing:

- **A mail server says only whether it took the email.** What became of it
  afterwards it does not tell CairnPost: an email's record stops at
  `sent`, no webhook about a delivery or a bounce is sent, and no address
  is suppressed by itself. Bounces come back as email to the sender's
  address. Suppress by hand what you learn there.
- If the server refuses one address of an email to several, the email is
  refused whole (422 `rejected_by_upstream`) and nothing is sent: nothing
  would ever say what became of the others. Take the address out, or
  suppress it.
- The connection is secured by STARTTLS unless `CAIRNPOST_SMTP_SECURITY`
  says otherwise, and a server that does not offer it is not used: the
  password is never said in the clear. The server's certificate must be
  valid for `CAIRNPOST_SMTP_HOST`. Sign-in is by `PLAIN` or `LOGIN`.
- A wrong password, a refused greeting, a server that wants a sign-in it
  was not given, or one that is down is never the email's fault: sends
  answer 503 and nothing is dropped. A refusal of
  the sender, a recipient or the message is the email's: 422. A connection
  lost after the message was handed over leaves it open whether it went:
  502, and a retry under the same `Idempotency-Key`.
- CairnPost writes the message itself: text and HTML for the reader to
  choose from, files the HTML shows, files beside it. `bcc` addresses are
  named to the server and written in no header. The email's Message-ID is
  CairnPost's own, under the sending domain.
- The server signs the mail (DKIM) and its addresses must be allowed to
  send for the domain (SPF): that is the server's setup, and
  `domains check` looks only for the domain's DMARC record here.
- Each send opens a connection of its own, and the whole exchange
  (connecting, securing, handing the message over, the server's answer)
  must fit in `CAIRNPOST_SEND_TIMEOUT`, 10 seconds unless set. Raise it
  for a slow server or large files: a send cut off after the message was
  handed over is one that may have gone.
- An address that is not plain ASCII is sent only through a server that
  says it carries such (SMTPUTF8); through another the email is refused.
- Set `CAIRNPOST_SMTP_HELLO` to the machine's full host name if it is
  not that already: some servers refuse a short one.

**`capture`** sends nothing. It keeps the last emails in memory and shows
them at `GET /capture/emails`: for development and tests. Of a file it
holds the name, type and size, not what is in it.

## Metrics

`GET /metrics` answers what a monitoring system reads, with an admin key
as the bearer token (in Prometheus, `authorization: {credentials: cp_...}`
in the scrape config):

| Metric | Says |
|---|---|
| `cairnpost_http_requests_total{route,status}` | Requests the API answered. |
| `cairnpost_http_request_seconds{route}` | How long they took, as a histogram. |
| `cairnpost_emails_last_day{state}` | Emails begun in the last 24 whole hours, by the state they are in now: each address an email went to is one. |
| `cairnpost_webhook_endpoints{state}` | Webhook endpoints that are `on`, `off` or `paused`. |
| `cairnpost_webhook_endpoints_failing` | Endpoints that are on and have failed every try since their last answer. |
| `cairnpost_provider_quota`, `cairnpost_provider_quota_used` | The mail provider's allowance, where it tells one. |
| `cairnpost_part_up{part}` | Whether delivery events are read, webhooks sent and old records pruned; a part that is off is not listed. |
| `cairnpost_build_info{version,driver}` | What is running. |

A route is the pattern that matched (`GET /emails/{id}`), never a path as
it was asked for, and no label holds an address, a subject, a URL or an
id. The request counts are one server's own, since it started; the rest
is read from the database when asked.

There is no number for the database: a scrape needs it to know the key,
so a scrape that fails is that signal (and `/health`, which needs no
key, says it outright). A key bound to a sending domain is told of that
domain's emails and endpoints only, and not of the request counts, which
would say how much others send.

## Console

The console is at `/console` on the same address as the API. Sign in with
an admin key; a key that may only send is refused. Its first page shows
how much was sent over the last 24 hours, 7 days or 30 days and what became
of it; the others list the emails on record and what became of each,
manage the suppression list and the webhook endpoints, check each sending
domain's DNS, and list, make and revoke API keys. A new key is shown once,
on the page that made it.

- The first page counts each address an email went to, as the metrics
  do: an email to three is three. It counts one in the hour or day it was sent,
  whenever its outcome came, and compares the period, up to its last
  whole hour, with the same stretch one period earlier. Emails the mail
  provider refused, or gave no answer about, are said apart.
  "Sent" is what the mail provider took; "delivered" includes emails
  marked as spam after they arrived. The numbers come from the send
  counts, so they reach back 30 days whatever `CAIRNPOST_RETENTION` is.
  Where delivery events are not read, outcomes are said to be not known.
- A sending domain is marked there when `CAIRNPOST_BOUNCE_WARN` percent
  (5) or more of its emails in the period bounced, once it sent at least
  50: fewer are not a rate. The page also shows the mail provider's
  allowance, asked for at most once a minute, the webhook endpoints that
  are failing or paused with how many webhooks each is owed, and whether
  each part of the server is working, which every page says in one line
  at the top.
- Times are said in the time zone of the browser that asks, and the
  first page's hours and days are that zone's. A browser that runs no
  scripts gets the server's own zone, which `TZ` sets, or
  `CAIRNPOST_CONSOLE_TIMEZONE` for the console alone; in a container
  with neither that is UTC. The image carries the time zones itself.
  The API and the commands say UTC whatever this is. Emails are counted
  by the hour of UTC, so in a zone half an hour off a day's edge on the
  first page is up to an hour off.

- A sign-in lasts `CAIRNPOST_CONSOLE_SESSION` (12 hours), ends when you
  sign out, and ends at once if the key is revoked. Make the setting
  shorter and the sign-ins older than it end too.
- An admin key bound to a sending domain sees, makes and revokes that
  domain's keys only, and sees that domain's emails, suppressions and
  webhook endpoints only.
- Emails are listed newest first, a hundred to a page, and can be narrowed
  to a state and to one recipient. An email to several addresses is one
  row that says how many more it went to, and its page shows what became
  of it at each. The address you look for is kept with
  your session on the server; the page's address carries only a random
  handle for it. So the address is in no proxy's log and not in the
  browser's history, it is forgotten when the session ends or when you
  ask to see everything again, and two tabs each keep their own search.
  A session keeps the twenty addresses looked for or shown last; a page
  narrowed to an older one says so and shows the whole list. The list
  never shows a subject.
- The suppression list shows who is not sent to and why, with the email
  it happened to: the first 500 in order of sending domain and address,
  narrowed to a domain or an address (`cairnpost suppressions list` on
  the server shows them all). An address is suppressed by hand or sent to again from
  there, and a sending domain's suppressions can be lifted together; both
  ask first. Each list, narrowed to an address, leads to the other
  narrowed to the same one, so one search answers "was it sent, did it
  arrive, is it blocked".
- A webhook endpoint is made, changed, turned off and on, tested and
  removed there. Its signing secret is shown once, on the page that made
  it, and can be changed, after asking, for a new one shown once. An
  endpoint paused for failing says so, and is resumed or turned off
  there. "Send a test" posts a signed `webhook.test` and says how the
  endpoint answered. Its page lists what it was sent, fifty to a page,
  and tries again what was not delivered.
- Domains lists the domains mail is sent from, and checks one's DNS: the
  records the mail provider asks for (for Cloudflare: its DKIM key, SPF
  and bounce address) and the DMARC policy, each in place, missing or not
  right, with what was found. A parent's DMARC record is named as one
  that may speak for a subdomain. It reads public DNS and asks the provider
  nothing, so "in place" means found and well formed, not that a key is
  the provider's own. A key bound to no domain can check any domain,
  also before anything is sent from it.
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
cairnpost domains check DOMAIN  see whether a domain's DNS is ready
cairnpost send                  send an email through a running server
cairnpost tail                  show what becomes of emails, as it happens
cairnpost version               print the version
```

`keys`, `suppressions`, `webhooks`, `tail` and `migrate` work on the database directly, so they run where the
server runs. `tail` shows the last delivery events and then each new one
(`-domain` for one sending domain's, `-last` for how many to begin with,
`-once` to stop after them); it ends by itself if the database stays
away. `send` takes `-to`, `-cc` and `-bcc`, each
one address or several with commas between, and `-attach` once for each
file to send with it. A key has a scope, `send` or `admin`, and may be bound to one
sending domain with `--domain`.

`domains check` reads public DNS only: it needs no database and no
credentials, so it can be run before anything else is set up. It looks for
the records of the provider named by `CAIRNPOST_DRIVER`, or by
`-provider` before the domain (`cairnpost domains check -provider
cloudflare mail.example.com`), and ends as a failure when one is missing,
or when no provider it knows is named. The bounce address must point at
the provider's own mail servers: a record that sends bounces elsewhere is
not right, since the server would never hear of them.

## Configuration

| Variable | Default | Purpose |
|---|---|---|
| `CAIRNPOST_POSTGRES_DSN` | required | The database. Postgres 18 or later. |
| `CAIRNPOST_DRIVER` | required | Who delivers the mail: `cloudflare`, `smtp` or `capture`. See Drivers. |
| `CAIRNPOST_SMTP_HOST` | required for `smtp` | The mail server to send through. |
| `CAIRNPOST_SMTP_SECURITY` | `starttls` | How the connection is secured: `starttls` (secured before anything is said, or not used), `tls` (from the first byte) or `none`. |
| `CAIRNPOST_SMTP_PORT` | `587`, `465` or `25` | The server's port: by default the one its security is spoken on. |
| `CAIRNPOST_SMTP_USERNAME`, `CAIRNPOST_SMTP_PASSWORD` | none | The sign-in, both or neither. The password is a secret, and is refused with `none`. |
| `CAIRNPOST_SMTP_HELLO` | the machine's name | The name the server is greeted with. |
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
| `CAIRNPOST_COUNTS_RETENTION` | `62d` | How long the counts of what was sent each hour, and what became of it, are kept: numbers only, apart from the records and whatever `CAIRNPOST_RETENTION` is. At least `62d`. `off` keeps them for ever. |
| `CAIRNPOST_PRUNE_EVERY` | `1h` | How long the pruner rests between passes. |
| `CAIRNPOST_IN_DOUBT_WINDOW` | `2m` | How long a send whose answer was lost blocks a retry of its key. After it, the retry is sent: an email that arrives twice is better than one that never does. Must be longer than the send timeout and shorter than the key's lifetime. It is also how long a send may stay `sending` before it is taken to be abandoned (by a server that stopped in the middle of it) and set to `unknown`: looked for that often, so within twice this time, and never sooner than `CAIRNPOST_SEND_TIMEOUT` plus a minute. |
| `CAIRNPOST_SEND_TIMEOUT` | `10s` | The most one call to the provider may take. |
| `CAIRNPOST_MAX_BODY_BYTES` | `1048576` | The largest request body accepted. |
| `CAIRNPOST_MAX_ATTACHMENTS` | `10` | How many files one email may carry (0 to 100; 0 takes none). |
| `CAIRNPOST_MAX_UPLOAD_BYTES` | `134217728` | How large the sends with files being taken at one time may be in all, as they arrive. A send past it answers 503 and is tried again. At least twice `CAIRNPOST_MAX_ATTACHMENT_BYTES`. |
| `CAIRNPOST_MAX_ATTACHMENT_BYTES` | `10485760` | How large an email's files may be in all, as they are, not as they travel (up to 64 MiB). A server holds a send's files in memory while it is sent: about five times their size for each send under way. |
| `CAIRNPOST_MAX_RECIPIENTS` | `50` | How many addresses one email may name, counting `to`, `cc` and `bcc` (1 to 1000). Set it to what the provider takes or below. |
| `CAIRNPOST_MAX_BATCH` | `100` | How many emails one `POST /emails/batch` may hold (1 to 1000). |
| `CAIRNPOST_BATCH_PARALLEL` | `4` | How many emails of a batch are handed to the provider at one time (1 to 64). Keep it below what the provider takes at once. |
| `CAIRNPOST_BATCH_TIMEOUT` | `1m` | How long a batch goes on starting emails, `10m` at most. An email whose turn has not come by then answers `not_started`. A proxy in front of the server must wait this long, and `CAIRNPOST_SEND_TIMEOUT` more, for an answer. |
| `CAIRNPOST_CAPTURE_KEEP` | `100` | How many emails the capture driver holds. |
| `CAIRNPOST_DNS_TIMEOUT` | `6s` | The most one check of a domain's DNS records may take, in the console and in `domains check`: a second at least, a minute at most. Raise it where the first DNS server is slow to answer. |
| `CAIRNPOST_DNS_SERVER` | the system's | The one DNS server those checks ask, as an IP address with or without a port (`1.1.1.1`). Set it where the machine's own resolver does not answer as the public internet does: a home router that makes up answers, or one that is silent about names that do not exist. |
| `CAIRNPOST_CONSOLE` | `on` | Serve the console at `/console`. `off` leaves it out. |
| `CAIRNPOST_CONSOLE_SESSION` | `12h` | How long a sign-in to the console lasts: a minute at least, 30 days at most. |
| `CAIRNPOST_BOUNCE_WARN` | `5` | The share of a sending domain's emails, in percent, that bounced at which the console's first page marks the domain. `off` marks none. |
| `CAIRNPOST_CONSOLE_TIMEZONE` | the system's | The time zone the console says times in for a browser that does not say its own, by name (`Europe/Berlin`, `UTC`). Unset, the system's own, which `TZ` sets. |
| `CAIRNPOST_CONSOLE_PLAIN_HTTP` | `off` | Let the console's session cookie travel without TLS, for a console opened over plain http in development. |
| `CAIRNPOST_MAX_KEYS` | `200` | How many keys that are not revoked one sending domain may have, and how many the keys bound to no domain may be. |
| `CAIRNPOST_WEBHOOK_TIMEOUT` | `10s` | The most one try at delivering a webhook may take. |
| `CAIRNPOST_WEBHOOK_RETRIES` | `5s,5m,30m,2h,5h,10h,10h` | The waits before each further try. A webhook is tried once, then once after each wait, then given up. |
| `CAIRNPOST_WEBHOOK_WORKERS` | `8` | How many webhooks are being sent at once, to all endpoints together. |
| `CAIRNPOST_WEBHOOK_PER_ENDPOINT` | `2` | How many are being sent at once to one endpoint. Under the number of workers, it keeps an endpoint that hangs from holding up the others. |
| `CAIRNPOST_WEBHOOK_MAX_ENDPOINTS` | `20` | How many endpoints may hear about one sending domain, and how many about all. |
| `CAIRNPOST_WEBHOOK_POLL` | `2s` | How long the sender rests when nothing is due. |
| `CAIRNPOST_WEBHOOK_PAUSE_AFTER` | `5d` | How long an endpoint may fail every try before a webhook given up pauses it: still owed what happens, sent nothing until it is resumed. In days or as a duration, `1h` at least; `off` never pauses. |
| `CAIRNPOST_WEBHOOK_SECRET_OVERLAP` | `24h` | How long, after an endpoint's signing secret is changed, webhooks are signed with the one before as well. In days or as a duration, `7d` at most; `off` ends the old one at once. |
| `CAIRNPOST_WEBHOOK_ALLOW_PRIVATE` | `off` | Let an endpoint be plain `http://` and at an address that is not on the public internet: for an application on the same private network. |

`cairnpost send` reads `CAIRNPOST_URL` and `CAIRNPOST_API_KEY`.

## Contributing

Commits, versions and releases follow [CONTRIBUTING.md](CONTRIBUTING.md).

## License

[MIT](LICENSE)
