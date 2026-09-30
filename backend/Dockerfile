# ── Stage 1: Build ────────────────────────────────────────────
FROM --platform=$BUILDPLATFORM golang:1.26.8-alpine AS builder
ARG TARGETOS TARGETARCH
WORKDIR /app
RUN apk add --no-cache git ca-certificates tzdata
COPY go.mod go.sum ./
RUN go mod download && go mod verify
COPY . .
RUN CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH go build \
    -ldflags="-w -s" \
    -trimpath \
    -o /workflow-server \
    ./cmd/server

# ── Stage 1b: Binary only ─────────────────────────────────────
# CI exports this stage per platform to check the binary's architecture; exporting
# `builder` would copy the whole Go toolchain and module cache out (minutes).
FROM scratch AS binaries
COPY --from=builder /workflow-server /workflow-server

# ── Stage 2: Minimal runtime ──────────────────────────────────
FROM alpine:3.22
RUN apk --no-cache add ca-certificates tzdata wget
WORKDIR /app
COPY --from=builder /workflow-server .
RUN adduser -D -u 10001 appuser
# A fresh named volume copies this directory's ownership, so task workspaces
# mounted here are writable by appuser.
RUN mkdir /workspaces && chown appuser:appuser /workspaces
USER appuser
EXPOSE 8080 9091
CMD ["./workflow-server"]
