#!/usr/bin/env bash
# Smoke test for a deployed private-rag instance.
#
# Usage:
#   PRIVATE_RAG_URL=https://rag.<fleet>.privasys.org \
#   PRIVATE_RAG_TOKEN=<jwt> \
#     ./scripts/smoke-test.sh
#
# Exits non-zero on the first failed assertion.

set -euo pipefail

URL="${PRIVATE_RAG_URL:-http://127.0.0.1:8443}"
TOKEN="${PRIVATE_RAG_TOKEN:?must be a Bearer JWT for an OIDC user the deployment trusts}"

red()   { printf '\033[31m%s\033[0m\n' "$*"; }
green() { printf '\033[32m%s\033[0m\n' "$*"; }
hr()    { printf -- '----- %s -----\n' "$*"; }

assert_eq() {
  local got="$1" want="$2" what="$3"
  if [[ "$got" != "$want" ]]; then
    red "FAIL: $what: got=$got want=$want"
    exit 1
  fi
}

curl_json() {
  curl -sS -H "Authorization: Bearer $TOKEN" -H "Content-Type: application/json" "$@"
}

hr "health"
got=$(curl -sS -o /dev/null -w '%{http_code}' "$URL/healthz")
assert_eq "$got" "200" "/healthz status"
got=$(curl -sS -o /dev/null -w '%{http_code}' "$URL/readyz")
assert_eq "$got" "200" "/readyz status"
green "ok"

hr "auth"
got=$(curl -sS -o /dev/null -w '%{http_code}' "$URL/api/v1/conversations")
assert_eq "$got" "401" "no Bearer -> 401"
green "ok"

hr "conversation lifecycle"
conv=$(curl_json -X POST "$URL/api/v1/conversations" -d '{"title":"smoke test"}')
conv_id=$(jq -r '.id' <<<"$conv")
[[ -n "$conv_id" && "$conv_id" != "null" ]] || { red "no conversation id in $conv"; exit 1; }
echo "  created conversation $conv_id"

list=$(curl_json "$URL/api/v1/conversations" | jq '.conversations | length')
[[ "$list" -ge 1 ]] || { red "list returned $list conversations"; exit 1; }
echo "  list has >=1 entries"

msg=$(curl_json -X POST "$URL/api/v1/conversations/$conv_id/messages" \
  -d '{"role":"user","content":"hello"}')
msg_id=$(jq -r '.id' <<<"$msg")
[[ -n "$msg_id" && "$msg_id" != "null" ]] || { red "no message id"; exit 1; }
echo "  appended message $msg_id"

asst=$(curl_json -X POST "$URL/api/v1/conversations/$conv_id/messages" \
  -d '{"role":"assistant","content":"world"}')
asst_id=$(jq -r '.id' <<<"$asst")

fb=$(curl_json -X PUT "$URL/api/v1/messages/$asst_id/feedback" \
  -d '{"rating":"good","comment":"smoke"}')
[[ "$(jq -r .rating <<<"$fb")" == "good" ]] || { red "feedback rating: $fb"; exit 1; }
echo "  upserted feedback"

curl_json -X DELETE "$URL/api/v1/conversations/$conv_id" >/dev/null
got=$(curl_json -o /dev/null -w '%{http_code}' "$URL/api/v1/conversations/$conv_id")
assert_eq "$got" "404" "deleted -> 404"
green "ok"

hr "mcp catalog"
catalog=$(curl_json "$URL/api/v1/mcp/tools")
n=$(jq '.tools | length' <<<"$catalog")
[[ "$n" == "4" ]] || { red "catalog has $n tools, want 4"; exit 1; }
green "ok ($n tools)"

hr "mcp search (stub returns empty)"
res=$(curl_json -X POST "$URL/api/v1/mcp/tools/search" -d '{"query":"anything"}')
hits=$(jq '.hits | length' <<<"$res")
assert_eq "$hits" "0" "stub search returns no hits"
green "ok"

green "ALL SMOKE TESTS PASSED against $URL"
