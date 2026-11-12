# Contributing to CairnPost

CairnPost is small on purpose. A change is welcome when it keeps it small and
sharp. These are the rules every commit, tag and release follows.

## Before a commit

```sh
gofmt -l .        # must print nothing
go vet ./...
go build ./...
go test ./...
```

Every commit on `main` builds and passes its tests on its own, not only the
last one of a branch.

## Commits

Commits follow [Conventional Commits](https://www.conventionalcommits.org/en/v1.0.0/):

```
<type>(<scope>): <subject>

<body: why the change is made, and what a reader could not guess>

<footers>
```

- **Types:** `feat` (something a user can now do), `fix` (something that was
  wrong), `docs`, `test`, `refactor` (no change in behaviour), `perf`, `build`
  (Dockerfile, dependencies, Go version), `ci`, `chore` (anything else).
- **Scope:** the package or area the change lives in, lowercase: `api`,
  `mail`, `keys`, `events`, `webhook`, `suppress`, `store`, `config`, `cli`,
  `driver/cloudflare`. Left out when the change has no single home.
- **Subject:** imperative, lowercase, no full stop, 72 characters at most.
  It says what the commit does: `feat(api): refuse a send to a suppressed address`.
- **Body:** wrapped at 72. The reason, the trade-off, what was measured.
  Not a list of files.
- **Breaking changes:** a `!` after the type or scope, and a
  `BREAKING CHANGE:` footer saying what a user must change.
- **One concern per commit.** A feature and an unrelated fix are two commits.
  A change and its tests go together.

## Branches

- `main` is always releasable. Nothing is committed to it directly except a
  release commit.
- Work happens on a branch named for it: `feat/webhooks`, `fix/idempotency-race`.
- A branch is merged with `git merge --no-ff`, so the history shows where each
  piece of work began and ended, and CI is green before and after.
- A merged branch is deleted.

## Go, Postgres and dependencies

- CairnPost targets the latest stable Go and the latest stable Postgres. The
  versions in `go.mod`, the Dockerfile, CI and the Compose file agree.
- **The standard library first.** A new dependency is the exception: open an
  issue saying what it replaces and what it costs before adding one. A pull
  request that adds one without that will be asked to remove it.

## Comments and documentation

- **Nothing in the repository points at something outside it.** A comment, a
  test name, a migration, a commit message or a doc never cites a private
  plan, a phase number, an internal decision or a conversation. It says the
  meaning and the reason in place, so that anyone with only this repository
  can follow it.
- **Documentation is true now.** A change to behaviour, configuration, an
  endpoint or a command updates the README, the docs and the changelog in the
  same commit.
- Planning notes and assistant instructions stay out of the repository; the
  `.gitignore` names them.

## The changelog

`CHANGELOG.md` follows [Keep a Changelog](https://keepachangelog.com/en/1.1.0/).
A commit that changes what a user can notice adds its line under
`## [Unreleased]` in the same commit, under one of: Added, Changed, Deprecated,
Removed, Fixed, Security. Lines are written for the person running CairnPost,
not for the person who wrote the code.

## Versions

CairnPost follows [Semantic Versioning](https://semver.org/spec/v2.0.0.html).
The version covers the HTTP API, the webhook payloads, the configuration, the
CLI and the database migrations.

- **v0.1.0** is the first version that works: it takes a send with an API key
  and hands it to an upstream. Versions count up from there.
- **Before v1.0.0:** a new feature or a breaking change raises the minor
  version (`v0.1.0` to `v0.2.0`), and the changelog says plainly what breaks.
  A release with fixes alone raises the patch (`v0.1.0` to `v0.1.1`).
- **v1.0.0** is the promise that the API is stable. From then on a breaking
  change raises the major version, a feature the minor, a fix the patch.
- **Pre-releases** are `v0.2.0-rc.1`, `v0.2.0-rc.2`. They never take the
  `latest` image tag.

## Tags

- A tag is annotated and named `vX.Y.Z`: `git tag -a v0.1.0 -m "CairnPost v0.1.0"`.
- A tag sits on a commit of `main` that CI has passed.
- **A pushed tag is never moved or deleted.** A release that was wrong is put
  right by the next patch version.
- Only releases are tagged.

## Cutting a release

1. On `main`, with CI green: rename `## [Unreleased]` in `CHANGELOG.md` to
   `## [X.Y.Z] - YYYY-MM-DD`, open a new empty `## [Unreleased]` above it, and
   update the compare links at the foot of the file.
2. Commit that alone: `chore(release): vX.Y.Z`.
3. Tag it: `git tag -a vX.Y.Z -m "CairnPost vX.Y.Z"`.
4. Push both: `git push origin main vX.Y.Z`.
5. The release workflow builds the image and pushes
   `ghcr.io/mettjs/cairnpost` as `X.Y.Z`, `X.Y` and, unless it is a
   pre-release, `latest`.
6. Publish the GitHub release from the tag, its notes being that version's
   section of the changelog: `gh release create vX.Y.Z --title "vX.Y.Z" --notes-file <section>`,
   with `--prerelease` for a release candidate.
7. Check the release: the image pulls, `cairnpost version` prints `X.Y.Z`.

The binary's version comes from the tag at build time, never from a constant
edited by hand.

## Migrations

All schema changes are versioned migrations. **A migration that shipped in a
release is never edited**; a mistake in one is corrected by a new migration.

## Scope

CairnPost sends transactional email and tells you what happened to it. It
never stores or logs a subject or a body, never tracks opens or clicks, and does not send newsletters or manage
audiences. Proposals that sharpen what it
does are welcome; proposals that widen it will likely be declined.
