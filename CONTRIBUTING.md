# Contributing to CairnPost

CairnPost is a small gateway for transactional email: one HTTP API in front
of the mail provider you choose, with API keys, idempotent sends and a record
of what became of each email. It is small on purpose, and a change is welcome
when it keeps it small and sharp.

## What it is not

- It keeps nothing of an email's subject or body, and never logs them.
- It does not track opens or clicks.
- It is for transactional email: no newsletters, no audiences.

Proposals that sharpen what it does are welcome; proposals that widen it
will likely be declined. If in doubt, open an issue before writing code.

## Development setup

```sh
docker compose up -d --wait postgres
cp .env.example .env
set -a && . ./.env && set +a
go run ./cmd/cairnpost serve
```

## Before opening a pull request

```sh
gofmt -l .        # must print nothing
go vet ./...
go build ./...
go test ./...
```

The integration tests run against a real Postgres and are behind a build tag:

```sh
docker compose up -d --wait postgres
export CAIRNPOST_TEST_DSN='postgres://cairnpost:cairnpost@localhost:5425/cairnpost?sslmode=disable'
go test -tags=integration -p 1 ./...
```

With the tag and no `CAIRNPOST_TEST_DSN` they fail rather than skip, so a run
that tested nothing never reads as green. CI runs all of the above.

## Conventions

- **Dependencies point inward:** `api → mail → { driver, store }`. Concrete
  implementations are built only in `cmd/cairnpost`. Only `internal/store`
  issues SQL, and only a driver names its provider's host.
- **Small files, one concern each.** No `utils` or `helpers` grab-bags.
- **The standard library first.** A new dependency is the exception: open an
  issue saying what it replaces before adding one.
- **The latest stable Go and Postgres.** The versions in `go.mod`, the
  Dockerfile, CI and the Compose file agree.
- **Schema changes are versioned migrations.** One that shipped in a release
  is never edited; a mistake is corrected by a new one.
- **Errors say what to do**, and never echo an address or a provider's text.
- **Documentation is true now.** A change to behaviour, configuration, an
  endpoint or a command updates the README and the changelog with it.
- **Commits** follow [Conventional Commits](https://www.conventionalcommits.org/en/v1.0.0/),
  one concern each, and say why. Versions follow
  [Semantic Versioning](https://semver.org/spec/v2.0.0.html), and the
  changelog follows [Keep a Changelog](https://keepachangelog.com/en/1.1.0/).
