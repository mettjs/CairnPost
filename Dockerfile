# syntax=docker/dockerfile:1
# Needs BuildKit, which Docker has used by default since version 23.
#
# Multi-stage build. Migrations are embedded in the binary, so the runtime
# image needs only the static binary and CA certificates.
#
# The build stage runs on the builder's own platform and cross-compiles for
# the target, and the runtime stage only copies files, so a build for
# another platform runs nothing under emulation.
FROM --platform=$BUILDPLATFORM golang:1.27-alpine AS build
WORKDIR /src

# Cache module downloads.
COPY go.mod go.sum ./
RUN go mod download

COPY . .
# VERSION is the release's tag; an untagged build says "dev".
ARG VERSION=dev
ARG TARGETOS
ARG TARGETARCH
RUN CGO_ENABLED=0 GOOS=${TARGETOS} GOARCH=${TARGETARCH} \
    go build -ldflags="-s -w -X main.version=${VERSION}" -o /out/cairnpost ./cmd/cairnpost

FROM alpine:3.24
# The certificates CairnPost trusts when it calls a mail provider.
COPY --from=build /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/ca-certificates.crt
COPY --from=build /out/cairnpost /usr/local/bin/cairnpost
# An unprivileged user, by number: no step runs in this stage to name one.
USER 10001:10001
EXPOSE 8080
ENTRYPOINT ["cairnpost"]
CMD ["serve"]
