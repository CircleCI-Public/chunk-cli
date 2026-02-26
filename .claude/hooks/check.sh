#!/usr/bin/env bash

set -e

bun run lint && bun run typecheck && bun run test:unit || exit 2
