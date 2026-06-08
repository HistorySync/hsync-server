#!/usr/bin/env bash
set -euo pipefail

BASE_URL="${BASE_URL:-http://localhost:8080}"
ADMIN_KEY="${ADMIN_KEY:?set ADMIN_KEY}"

curl --request POST \
  --url "${BASE_URL}/api/v1/admin/ops/check" \
  --header "X-Admin-Key: ${ADMIN_KEY}" \
  --header "Content-Type: application/json"
