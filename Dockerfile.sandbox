# syntax=docker/dockerfile:1
#
# debian:bookworm-slim base gives us glibc so downloaded agent binaries
# (e.g. the CircleCI sandbox agent) can execute. Alpine's musl libc
# would cause those binaries to fail with "no such file or directory".

FROM debian:bookworm-slim

ENV GOTOOLCHAIN=auto \
    CGO_ENABLED=1 \
    GOPATH=/go \
    PATH=/usr/local/go/bin:/go/bin:/usr/local/bin:$PATH

# Build tools + runtime deps
RUN apt-get update && apt-get install -y --no-install-recommends \
    git \
    gcc \
    libc6-dev \
    ca-certificates \
    curl \
    && rm -rf /var/lib/apt/lists/*

# Install Go, then strip files not needed at runtime.
# Symlink binaries into /usr/local/bin so they're on the default PATH
# even when the sandbox agent runs commands in a clean environment.
RUN curl -fsSL https://go.dev/dl/go1.26.1.linux-amd64.tar.gz \
    | tar -C /usr/local -xz \
 && rm -rf /usr/local/go/test /usr/local/go/api /usr/local/go/doc /usr/local/go/misc \
 && ln -s /usr/local/go/bin/go /usr/local/bin/go \
 && ln -s /usr/local/go/bin/gofmt /usr/local/bin/gofmt

# Install task from pre-built release binary
RUN curl -sSL https://taskfile.dev/install.sh | sh -s -- -d -b /usr/local/bin

# git identity required by tests that run git commit
RUN git config --global user.email "sandbox@chunk.ci" \
 && git config --global user.name "Chunk Sandbox"

WORKDIR /workspace
