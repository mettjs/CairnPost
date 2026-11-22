# Client cases

The cases every CairnPost client is held to, whatever language it is
written in. A client that passes them sends what the server takes, reads
what the server answers, and refuses what is not the server's.

They are plain JSON, so a client needs nothing but its own test runner to
read them. The Go, Node.js and Python clients each hold a copy and run it
in their tests. This folder is the one that is changed; the copies follow.

The server's own tests hold these files to the server. Every call is a
route with the key the route asks, and sends a body and a query the route
takes. Every answer a client must take fits what
[`openapi.json`](../openapi.json) describes for that call and status.
Every error name the API describes has a case, on a call and with a status
the API describes it for. Every webhook a client must read is signed as
the server signs and, when it is of a type the server sends, has the body
the server writes.

## The files

| File | What it holds |
|---|---|
| `calls.json` | Every call a client has: the route, the key it needs, what goes on the wire for it, and an answer of the server's that the client must take. |
| `answers.json` | Answers a client must refuse, and answers it must take. |
| `errors.json` | Refusals, and what the client must say of each. |
| `webhooks.json` | Webhooks, each with its headers and body, and whether it is read or why it is refused. |

A runner fails when a file holds a field it does not know, names a call
the client lacks, or holds no case at all: a case must never pass because
part of it was not looked at.

## An answer

Wherever a case has a server answer, the answer is written as:

```json
{"status": 200, "json": {"id": "..."}, "headers": {"Retry-After": "7"}}
```

`json` is the body as JSON; `text` in its place is the body as it is, for
what is not JSON. An answer has one of the two. `headers` is optional. A
`json` answer is sent as `application/json`.

## calls.json

```json
{
  "name": "emails.cancel",
  "route": "POST /emails/{id}/cancel",
  "key": "any",
  "sends": {"method": "POST", "path": "/emails/01a2.../cancel"},
  "answer": {"status": 200, "json": {"id": "01a2...", "state": "cancelled"}}
}
```

- `name` is the call's name in the other files. A client keeps a table
  from each name to the call that makes this request.
- `key` is `none`, `any` or `admin`. With `none` the client sends no
  `Authorization` header; otherwise it sends `Bearer` and its key.
- `sends` is the one request the call makes: `method`, `path` as it
  travels, `query` as names and values (none when left out),
  `idempotency_key` as the `Idempotency-Key` header (none when left out),
  and `body` as JSON (no body and no `Content-Type` when left out; with
  one, `application/json`). A time in a body is the instant it names,
  however it is written.
- `answer` must be taken without an error.

## answers.json

`refused` and `taken` are lists of:

```json
{"name": "a lift of none", "calls": ["suppressions.lift"], "answer": {"status": 200, "json": {"lifted": 0}}}
```

`calls` is a list of names, or `"all"` for every call. Each call is made
as `calls.json` has it, against a server that gives the answer. Under
`refused` the client must fail with its error for an answer that is not
CairnPost's. Under `taken` it must not fail.

## errors.json

```json
{
  "name": "the same send still under way",
  "call": "emails.send",
  "answer": {"status": 409, "json": {"statusCode": 409, "name": "concurrent_idempotent_requests", "message": "In flight."}, "headers": {"Retry-After": "7"}},
  "want": {"status": 409, "code": "concurrent_idempotent_requests", "detail": "In flight.", "email_id": null,
           "retry_after": 7, "retryable": true, "may_have_sent": false, "not_found": false, "wrong_address": false}
}
```

The call must fail with the client's error for a refusal, and that error
must say every field of `want`: `code` and `detail` are the server's
`name` and `message`, `null` when the answer is not CairnPost's own;
`retry_after` is in seconds. When `code` is `null`, the error must not
repeat the answer's `message` anywhere: what is not CairnPost's own could
be an echo of the email.

## webhooks.json

`secret` is the endpoint's signing secret and `now` the clock, in Unix
seconds, for every case. `secrets` lists values a verifier must take as a
secret and values it must refuse. Each case is:

```json
{
  "name": "an old webhook",
  "headers": {"webhook-id": "dlv-1", "webhook-timestamp": "1893455709", "webhook-signature": "v1,..."},
  "body": "{\"type\":\"email.bounced\", ...}",
  "tolerance": 300,
  "want": {"refused": "stale"}
}
```

`body` is the request's body, byte for byte. `tolerance` is how far the
timestamp may be from `now`, in seconds; left out, the client's own (five
minutes). `want` is one of:

- `{"refused": why}`, where `why` is `unsigned`, `signature`, `stale` or
  `payload`.
- `{"event": {...}}`: the event as read. `id`, `type` and `at` always;
  `email`, `suppression` or `received` when the client gives one, and
  absent when it gives none. Every field of those is written: a text the
  client has none of is `""`, `bounce` is the bounce's type, and a time is
  its instant in UTC to the second, `""` when there is none.

A case's signature is not made up, nor is the body of an event the server
sends. When a case is added, the server's test
(`go test ./internal/webhook/`) says which signature it should carry and
which body it should have. A type the server does not send (`email.opened`,
say) stands for a newer server's: a client passes it on with what it could
read.

## Changing a case

Change it here, with the server change it belongs to. Run `go test ./...`.
Then copy the four JSON files into each client (`testdata/cases` in Go,
`test/cases` in Node.js, `tests/cases` in Python), name this commit in the
copy's `SOURCE` file, and run the client's tests. Each client's CI
compares its copy with the commit it names.
