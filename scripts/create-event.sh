#!/usr/bin/env bash
# Creates an event on the deployed contract as the host (testnet).
#   CONTRACT=doorlist.you.testnet HOST=ines.testnet ID=builders-night TITLE="Builders Night" \
#   DEPOSIT_USDC=10 STARTS="2026-11-20T19:00:00Z" HOURS=3 CAPACITY=40 DAO=lisbon-builders.sputnikv2.testnet ./scripts/create-event.sh
set -euo pipefail
: "${CONTRACT:?}" "${HOST:?}" "${ID:?}" "${TITLE:?}" "${STARTS:?}" "${DAO:?}"
ARGS=$(python3 - <<PY
import json, datetime
s = int(datetime.datetime.fromisoformat("$STARTS".replace("Z", "+00:00")).timestamp() * 1000)
print(json.dumps({"input": {"id": "$ID", "title": "$TITLE", "deposit": str(int(float("${DEPOSIT_USDC:-10}") * 1_000_000)),
  "starts_at_ms": s, "checkin_close_ms": s + int("${HOURS:-3}") * 3600000,
  "public_from_ms": s - int("${PRIORITY_HOURS:-0}") * 3600000 if int("${PRIORITY_HOURS:-0}") else 0,
  "priority_min_shows": int("${PRIORITY_MIN_SHOWS:-0}"), "capacity": int("${CAPACITY:-40}"), "dao": "$DAO"}}))
PY
)
near contract call-function as-transaction "$CONTRACT" create_event json-args "$ARGS" \
  prepaid-gas '30 Tgas' attached-deposit '0 NEAR' sign-as "$HOST" network-config testnet sign-with-keychain send
