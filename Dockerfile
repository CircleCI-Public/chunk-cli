# syntax=docker/dockerfile:1
FROM golang:1.24-bookworm AS builder

ENV GOTOOLCHAIN=auto

WORKDIR /src

COPY go.mod go.sum ./
RUN go mod download

COPY . .
RUN go build -o /dist/chunk ./cmd/chunk

FROM debian:bookworm-slim

RUN apt-get update && apt-get install -y --no-install-recommends \
    ca-certificates \
    git \
    && update-ca-certificates \
    && rm -rf /var/lib/apt/lists/*

COPY --from=builder /dist/chunk /usr/local/bin/chunk

# API keys are injected at runtime — never baked into the image.
ENV ANTHROPIC_API_KEY=""
ENV GITHUB_TOKEN=""

CMD ["chunk", "--help"]
