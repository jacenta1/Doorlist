#!/usr/bin/env bash
# Creates the Sputnik DAO that holds an event community's pot (testnet).
# Council = the hosts. The Doorlist contract gets a "doorlist" role that can
# only add and approve Transfer proposals.
#
#   NAME=lisbon-builders COUNCIL='["ines.testnet","marta.testnet"]' \
#   CONTRACT=doorlist.you.testnet SIGNER=ines.testnet TOKEN=usdc.fakes.testnet ./scripts/create-dao.sh
set -euo pipefail
: "${NAME:?dao name}" "${COUNCIL:?json array of council accounts}" "${CONTRACT:?doorlist contract}" "${SIGNER:?account paying for creation}"
FACTORY="${FACTORY:-sputnikv2.testnet}"
TOKEN="${TOKEN:-usdc.fakes.testnet}"
ARGS=$(python3 - <<PY
import json, base64
policy = {
  "roles": [
    {"name": "council", "kind": {"Group": json.loads('$COUNCIL')}, "permissions": ["*:*"], "vote_policy": {}},
    {"name": "doorlist", "kind": {"Group": ["$CONTRACT"]},
     "permissions": ["transfer:AddProposal", "transfer:VoteApprove"],
     "vote_policy": {"transfer": {"weight_kind": "RoleWeight", "quorum": "0", "threshold": [1, 1]}}},
  ],
  "default_vote_policy": {"weight_kind": "RoleWeight", "quorum": "0", "threshold": [1, 2]},
  "proposal_bond": "0", "proposal_period": "604800000000000",
  "bounty_bond": "100000000000000000000000", "bounty_forgiveness_period": "86400000000000",
}
inner = {"config": {"name": "$NAME", "purpose": "Holds Doorlist event deposits", "metadata": ""}, "policy": policy}
print(json.dumps({"name": "$NAME", "args": base64.b64encode(json.dumps(inner).encode()).decode()}))
PY
)
near contract call-function as-transaction "$FACTORY" create json-args "$ARGS" \
  prepaid-gas '150 Tgas' attached-deposit '6 NEAR' sign-as "$SIGNER" network-config testnet sign-with-keychain send
near contract call-function as-transaction "$TOKEN" storage_deposit json-args "{\"account_id\":\"$NAME.$FACTORY\"}" \
  prepaid-gas '30 Tgas' attached-deposit '0.00125 NEAR' sign-as "$SIGNER" network-config testnet sign-with-keychain send
echo "DAO: $NAME.$FACTORY"
