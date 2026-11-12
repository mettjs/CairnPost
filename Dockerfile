# Multi-stage build. Migrations are embedded in the binary, so the runtime
# image needs only the static binary and CA certificates.
FROM golang:1.27-alpine AS build
WORKDIR /src

# Cache module downloads.
COPY go.mod go.sum ./
RUN go mod download

COPY . .
# VERSION is the release's tag; an untagged build says "dev".
ARG VERSION=dev
RUN CGO_ENABLED=0 go build -ldflags="-s -w -X main.version=${VERSION}" -o /out/cairnpost ./cmd/cairnpost

FROM alpine:3.24
RUN apk add --no-cache ca-certificates \
    && adduser -D -u 10001 cairnpost
USER cairnpost
COPY --from=build /out/cairnpost /usr/local/bin/cairnpost
EXPOSE 8080
ENTRYPOINT ["cairnpost"]
CMD ["serve"]
