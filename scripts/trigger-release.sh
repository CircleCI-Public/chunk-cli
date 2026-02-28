#!/usr/bin/env bash
set -euo pipefail

CIRCLECI_TOKEN="${CIRCLECI_TOKEN:?CIRCLECI_TOKEN is required}"

PROJECT_SLUG="github/CircleCI-Public/chunk-cli"
BRANCH="main"
DEFINITION_ID="7a093425-e5d7-4d4e-b178-d049d0c35f0d"

curl -sSL \
  --request POST \
  --url "https://circleci.com/api/v2/project/${PROJECT_SLUG}/pipeline/run" \
  --header "Circle-Token: ${CIRCLECI_TOKEN}" \
  --header "Content-Type: application/json" \
  --data "{
    \"checkout\": {\"branch\": \"${BRANCH}\"},
    \"config\": {\"branch\": \"${BRANCH}\"},
    \"definition_id\": \"${DEFINITION_ID}\"
  }"
