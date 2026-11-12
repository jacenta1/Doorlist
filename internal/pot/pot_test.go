package pot

import (
	"encoding/base64"
	"encoding/json"
	"strings"
	"testing"
)

func TestUSDC(t *testing.T) {
	cases := map[uint64]string{0: "0.00", 3_200_000: "3.20", 12_903_225: "12.903225", 10_000_000: "10.00", 1: "0.000001"}
	for in, want := range cases {
		if got := USDC(in); got != want {
			t.Errorf("USDC(%d) = %s, want %s", in, got, want)
		}
	}
	if USDC2(12_903_225) != "12.90" || USDC2(5) != "0.00" {
		t.Fatal("USDC2")
	}
}

func TestParseUSDC(t *testing.T) {
	ok := map[string]uint64{"10": 10_000_000, "0.25": 250_000, "1.5": 1_500_000, ".5": 500_000}
	for in, want := range ok {
		if got, good := ParseUSDC(in); !good || got != want {
			t.Errorf("ParseUSDC(%q) = %d %v", in, got, good)
		}
	}
	for _, bad := range []string{"", "abc", "1.0000001", "-1"} {
		if _, good := ParseUSDC(bad); good {
			t.Errorf("ParseUSDC(%q) accepted", bad)
		}
	}
}

func TestProjections(t *testing.T) {
	p := Projections(10_000_000, 40, 10, 20)
	// 10% of 40 flake = 4 flakes, 36 shows: 40/36 = 1.11 each.
	if p[0].BonusText != "+1.11" || p[1].Shows != 32 {
		t.Fatalf("%+v", p)
	}
}

func TestDAOPolicy(t *testing.T) {
	raw := CreateDAOArgs("lisbon-builders", "Doorlist pots", []string{"ines.near"}, "doorlist.near")
	var outer struct{ Name, Args string }
	_ = json.Unmarshal(raw, &outer)
	inner, _ := base64.StdEncoding.DecodeString(outer.Args)
	s := string(inner)
	for _, want := range []string{`"transfer:AddProposal"`, `"transfer:VoteApprove"`, `"Group":["doorlist.near"]`, `"proposal_bond":"0"`} {
		if !strings.Contains(s, want) {
			t.Errorf("policy missing %s", want)
		}
	}
	// The doorlist role must not be able to do anything but transfers.
	if strings.Contains(s, `"Group":["doorlist.near"]},"name":"doorlist","permissions":["*`) {
		t.Fatal("doorlist role too broad")
	}
}
