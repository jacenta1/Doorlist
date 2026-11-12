#!/usr/bin/env bash
# Deploys the Doorlist contract to NEAR testnet and initialises it.
# Nothing here touches mainnet. Requires near-cli-rs (`near`) and cargo-near,
# and an account you are signed in to (`near account import-account ...`).
#
#   CONTRACT=doorlist.you.testnet OWNER=you.testnet RELAYER=relay.you.testnet \
#   TOKEN=usdc.fakes.testnet ./scripts/deploy-testnet.sh
set -euo pipefail
: "${CONTRACT:?account to deploy to}" "${OWNER:?owner account}" "${RELAYER:?relayer account}"
TOKEN="${TOKEN:-usdc.fakes.testnet}"
cd "$(dirname "$0")/../contract/rust"
cargo near build non-reproducible-wasm
near contract deploy "$CONTRACT" use-file target/near/doorlist_contract.wasm \
  with-init-call init json-args "{\"owner\":\"$OWNER\",\"token\":\"$TOKEN\",\"relayer\":\"$RELAYER\"}" \
  prepaid-gas '30 Tgas' attached-deposit '0 NEAR' \
  network-config testnet sign-with-keychain send
# The contract forwards deposits, so it needs storage on the token.
near contract call-function as-transaction "$TOKEN" storage_deposit json-args "{\"account_id\":\"$CONTRACT\"}" \
  prepaid-gas '30 Tgas' attached-deposit '0.00125 NEAR' sign-as "$OWNER" network-config testnet sign-with-keychain send
echo "Deployed. Set DOORLIST_MODE=live DOORLIST_CONTRACT=$CONTRACT in .env"
