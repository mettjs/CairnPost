# CairnPost

> A cairn marks the way. A post carries word along it.

A small, self-hostable transactional email service: one HTTP API, API keys,
idempotent sends, delivery events as signed webhooks and a suppression list,
in front of the mail provider you choose. Written in Go, backed by Postgres.

**Status: not yet usable.** Nothing is released. The first version will be
v0.1.0; see [CHANGELOG.md](CHANGELOG.md).

## What it is, and is not

- It is a gateway. It owns the API, the keys, the records and the tools; an
  upstream provider delivers the mail.
- It never stores or logs an email's subject or body.
- It does not track opens or clicks, and it does not send newsletters.
- It comes with a small console to manage keys, domains and suppressions,
  served by the same binary.
- It leans on Go's standard library and keeps its dependencies to a minimum.

## Contributing

Commits, versions and releases follow [CONTRIBUTING.md](CONTRIBUTING.md).

## License

[MIT](LICENSE)
