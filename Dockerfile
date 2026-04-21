# syntax=docker/dockerfile:1
#
# Minimal self-improving agent. The runtime image carries git and a Go
# toolchain because the agent rewrites its own source via 'git apply' and
# 'go build' inside the container. That's intentional: once the agent
# starts adding features (HTTP server, SQLite, Docker sandbox, ...) it
# needs the toolchain to rebuild itself in place.

# Build stage: compile the bootstrap binary.
FROM --platform=$BUILDPLATFORM golang:1.23-alpine AS build
ARG TARGETOS
ARG TARGETARCH
WORKDIR /src
COPY go.mod ./
COPY *.go ./
RUN CGO_ENABLED=0 GOOS=${TARGETOS} GOARCH=${TARGETARCH} \
    go build -trimpath -ldflags="-s -w" -o /out/daemon .

# Runtime stage.
FROM golang:1.23-alpine
RUN apk add --no-cache git ca-certificates bash
WORKDIR /app
COPY --from=build /out/daemon /app/daemon
COPY *.go /app/
COPY go.mod /app/
COPY .env.example /app/.env.example
ENTRYPOINT ["/app/daemon", "--self-src", "/app"]
