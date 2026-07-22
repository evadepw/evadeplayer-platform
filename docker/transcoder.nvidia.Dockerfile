FROM golang:1.25-alpine AS builder
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY cmd ./cmd
COPY internal ./internal
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/transcoder ./cmd/transcoder

FROM nvidia/cuda:12.6.0-runtime-ubuntu24.04
RUN apt-get update && apt-get install -y --no-install-recommends \
    ca-certificates tzdata ffmpeg \
    && rm -rf /var/lib/apt/lists/*
WORKDIR /app
COPY --from=builder /out/transcoder /app/transcoder
ENTRYPOINT ["/app/transcoder"]
