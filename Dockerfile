# syntax=docker/dockerfile:1

# Build stage.
FROM --platform=$BUILDPLATFORM golang:1.23-alpine AS build
ARG TARGETOS
ARG TARGETARCH
WORKDIR /src
COPY go.mod go.sum* ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 GOOS=${TARGETOS} GOARCH=${TARGETARCH} \
    go build -trimpath -ldflags="-s -w" -o /out/daemon .

# Runtime stage.
FROM alpine:3.20
RUN apk add --no-cache docker-cli git ca-certificates bash
WORKDIR /app
COPY --from=build /out/daemon /usr/local/bin/daemon
COPY sandbox.Dockerfile ./sandbox.Dockerfile
COPY templates ./templates
COPY .env.example ./.env.example
EXPOSE 8080
ENTRYPOINT ["/usr/local/bin/daemon"]
