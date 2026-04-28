#!/usr/bin/env bash
# Deploy private-rag (+ its colocated pgvector) to an Enclave OS Virtual fleet.
#
# Talks to the fleet's manager at MANAGER_URL (RA-TLS terminated by the gateway,
# Bearer-token authenticated). The manager pulls + starts both containers.
#
# Required environment:
#   MANAGER_URL     e.g. https://manager.<fleet>.privasys.org
#   MANAGER_TOKEN   bearer token the orchestrator holds (NOT a user JWT)
#   FLEET_HOST      external FQDN that will route to private-rag (e.g. rag.<fleet>.privasys.org)
#   PG_PASSWORD     per-fleet Postgres password (generate once, persist via vault)
#   OIDC_ISSUER     comma-separated issuer URLs trusted by this fleet
#   OIDC_AUDIENCE   audience claim the gateway sets on tokens forwarded here
#   PRIVATE_RAG_DIGEST   ghcr.io/privasys/private-rag@sha256:...
#   PGVECTOR_DIGEST      docker.io/pgvector/pgvector@sha256:...
#
# Optional:
#   CORS_ORIGINS         default: https://chat.privasys.org,https://chat.privasys.id
#   STORAGE_SIZE         default: 10G  (size of the per-container LUKS volume for PGDATA)

set -euo pipefail

: "${MANAGER_URL:?}"
: "${MANAGER_TOKEN:?}"
: "${FLEET_HOST:?}"
: "${PG_PASSWORD:?}"
: "${OIDC_ISSUER:?}"
: "${OIDC_AUDIENCE:?}"
: "${PRIVATE_RAG_DIGEST:?}"
: "${PGVECTOR_DIGEST:?}"

CORS_ORIGINS="${CORS_ORIGINS:-https://chat.privasys.org,https://chat.privasys.id}"
STORAGE_SIZE="${STORAGE_SIZE:-10G}"

post() {
  local body="$1"
  curl -sS -fail \
    -H "Authorization: Bearer $MANAGER_TOKEN" \
    -H "Content-Type: application/json" \
    -X POST "$MANAGER_URL/api/v1/containers" \
    -d "$body"
  echo
}

echo "==> Loading rag-db ($PGVECTOR_DIGEST)"
post "$(jq -n \
  --arg image "$PGVECTOR_DIGEST" \
  --arg storage "$STORAGE_SIZE" \
  --arg pw "$PG_PASSWORD" '
{
  name: "rag-db",
  image: $image,
  port: 5432,
  internal: true,
  storage: $storage,
  env: {
    POSTGRES_PASSWORD: $pw,
    POSTGRES_DB:       "rag",
    PGDATA:            "/data/pgdata"
  }
}')"

echo "==> Loading private-rag ($PRIVATE_RAG_DIGEST)"
post "$(jq -n \
  --arg image "$PRIVATE_RAG_DIGEST" \
  --arg host "$FLEET_HOST" \
  --arg cors "$CORS_ORIGINS" \
  --arg dsn  "postgres://postgres:${PG_PASSWORD}@rag-db:5432/rag?sslmode=disable" \
  --arg iss  "$OIDC_ISSUER" \
  --arg aud  "$OIDC_AUDIENCE" '
{
  name: "private-rag",
  image: $image,
  port: 8443,
  hostname: $host,
  env: {
    PRIVATE_RAG_LISTEN:       ":8443",
    PRIVATE_RAG_CORS_ORIGINS: $cors,
    DATABASE_URL:             $dsn,
    OIDC_ISSUER:              $iss,
    OIDC_AUDIENCE:            $aud
  },
  health_check: { path: "/readyz", interval: "5s", timeout: "3s", retries: 20 },
  wait_ready: true
}')"

echo "==> Done. Verify with:"
echo "    PRIVATE_RAG_URL=https://$FLEET_HOST PRIVATE_RAG_TOKEN=<jwt> ./scripts/smoke-test.sh"
