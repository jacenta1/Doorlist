// Package pot is the money side: USDC formatting, the payout split as people
// read it, and the Sputnik DAO that holds each pot.
package pot

import (
	"encoding/base64"
	"encoding/json"
	"strconv"
	"strings"

	"doorlist/contract/core"
)

// USDC formats micro-USDC as a decimal with two to six places: 12903225 → "12.903225",
// 3200000 → "3.20".
func USDC(micro uint64) string {
	whole := micro / 1_000_000
	frac := micro % 1_000_000
	f := strconv.FormatUint(frac+1_000_000, 10)[1:] // zero-padded 6 digits
	f = strings.TrimRight(f, "0")
	for len(f) < 2 {
		f += "0"
	}
	return strconv.FormatUint(whole, 10) + "." + f
}

// USDC2 formats with exactly two places, rounding down: for headlines.
func USDC2(micro uint64) string {
	cents := micro / 10_000
	return strconv.FormatUint(cents/100, 10) + "." + strconv.FormatUint(cents%100+100, 10)[1:]
}

// ParseUSDC reads "10", "10.5", "0.25" into micro-USDC.
func ParseUSDC(s string) (uint64, bool) {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0, false
	}
	whole, frac, _ := strings.Cut(s, ".")
	if len(frac) > 6 {
		return 0, false
	}
	w, err := strconv.ParseUint(whole, 10, 64)
	if err != nil && whole != "" {
		return 0, false
	}
	frac += strings.Repeat("0", 6-len(frac))
	f, err := strconv.ParseUint(frac, 10, 64)
	if err != nil {
		return 0, false
	}
	return w*1_000_000 + f, true
}

// Outlook is what a guest sees before the event: "if 20% flake, you get +2.50".
type Outlook struct {
	FlakeRate int    // percent
	Shows     uint32 // expected shows
	Bonus     uint64 // micro-USDC on top of the deposit
	BonusText string
}

// Projections returns outlooks for a few flake rates, given who is going now.
func Projections(deposit uint64, going uint32, rates ...int) []Outlook {
	out := []Outlook{}
	for _, r := range rates {
		flakes := uint32(int(going) * r / 100)
		shows := going - flakes
		s := core.Split(deposit, going, shows)
		out = append(out, Outlook{FlakeRate: r, Shows: shows, Bonus: s.SharePerShow, BonusText: "+" + USDC2(s.SharePerShow)})
	}
	return out
}

// ---- Sputnik DAO ----

// Policy is a Sputnik DAO v2 policy for an event community:
//   - "council": the hosts, who can do everything with a 1/2 vote;
//   - "doorlist": only the Doorlist contract, which can add and approve
//     Transfer proposals (and nothing else), so payouts run without a vote.
//
// The proposal bond is zero so the contract can file one proposal per guest.
func Policy(council []string, doorlistContract string) map[string]any {
	half := map[string]any{"weight_kind": "RoleWeight", "quorum": "0", "threshold": []int{1, 2}}
	return map[string]any{
		"roles": []any{
			map[string]any{
				"name": "council", "kind": map[string]any{"Group": council},
				"permissions": []string{"*:*"}, "vote_policy": map[string]any{},
			},
			map[string]any{
				"name": "doorlist", "kind": map[string]any{"Group": []string{doorlistContract}},
				"permissions": []string{"transfer:AddProposal", "transfer:VoteApprove"},
				"vote_policy": map[string]any{"transfer": map[string]any{"weight_kind": "RoleWeight", "quorum": "0", "threshold": []int{1, 1}}},
			},
		},
		"default_vote_policy":       half,
		"proposal_bond":             "0",
		"proposal_period":           "604800000000000",
		"bounty_bond":               "100000000000000000000000",
		"bounty_forgiveness_period": "86400000000000",
	}
}

// CreateDAOArgs are the arguments of the Sputnik factory's `create` method.
// name becomes <name>.<factory>.
func CreateDAOArgs(name, purpose string, council []string, doorlistContract string) []byte {
	inner, _ := json.Marshal(map[string]any{
		"config": map[string]any{"name": name, "purpose": purpose, "metadata": ""},
		"policy": Policy(council, doorlistContract),
	})
	out, _ := json.Marshal(map[string]any{"name": name, "args": base64.StdEncoding.EncodeToString(inner)})
	return out
}

// Itoa formats an unsigned integer.
func Itoa(v uint64) string { return strconv.FormatUint(v, 10) }
