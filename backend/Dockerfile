# ── Stage 1: Build ────────────────────────────────────────────
FROM golang:1.26.1-alpine AS builder
WORKDIR /app
RUN apk add --no-cache git ca-certificates tzdata
COPY go.mod go.sum ./
RUN go mod download && go mod verify
COPY . .
RUN CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build \
    -ldflags="-w -s" \
    -trimpath \
    -o /workflow-server \
    ./cmd/server

# ── Stage 2: Minimal runtime ──────────────────────────────────
FROM alpine:3.19
RUN apk --no-cache add ca-certificates tzdata wget
WORKDIR /app
COPY --from=builder /workflow-server .
RUN adduser -D -u 10001 appuser
USER appuser
EXPOSE 8080 9090 9091
CMD ["./workflow-server"]
