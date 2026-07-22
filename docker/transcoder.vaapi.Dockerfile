FROM golang:1.25-alpine AS builder
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY cmd ./cmd
COPY internal ./internal
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/transcoder ./cmd/transcoder

FROM --platform=linux/amd64 ubuntu:24.04
RUN sed -i 's/^Components: .*/Components: main restricted universe multiverse/' /etc/apt/sources.list.d/ubuntu.sources \
    && apt-get update && apt-get install -y --no-install-recommends \
    ca-certificates tzdata ffmpeg \
    libva-drm2 mesa-va-drivers intel-media-va-driver-non-free \
    && rm -rf /var/lib/apt/lists/*
WORKDIR /app
COPY --from=builder /out/transcoder /app/transcoder
ENTRYPOINT ["/app/transcoder"]
