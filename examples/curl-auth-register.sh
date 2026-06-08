#!/usr/bin/env bash
set -euo pipefail

BASE_URL="${BASE_URL:-http://localhost:8080}"

curl --request POST \
  --url "${BASE_URL}/api/v1/auth/register" \
  --header "Content-Type: application/json" \
  --data '{
    "email": "alice@example.com",
    "password": "correct horse battery staple",
    "display_name": "Alice",
    "turnstile_token": "test-turnstile-token"
  }'
