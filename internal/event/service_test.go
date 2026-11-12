package event

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"doorlist/contract/core"
	"doorlist/internal/checkin"
	"doorlist/internal/intents"
)

type fakeQuoter struct{ last intents.QuoteRequest }

func (f *fakeQuoter) Quote(_ context.Context, r intents.QuoteRequest) (*intents.QuoteResponse, error) {
	f.last = r
	o, _ := intents.OriginByID("usdc-base")
	return intents.OfflineQuote(o, r), nil
}
func (f *fakeQuoter) Status(context.Context, string) (string, error) { return "PENDING_DEPOSIT", nil }

var fixedNow = time.Date(2026, 11, 13, 18, 25, 0, 0, time.UTC)

func demo(t *testing.T) (*Service, *SimChain, *fakeQuoter, *time.Time) {
	t.Helper()
	now := fixedNow
	clock := func() time.Time { return now }
	chain := NewSimChain(DemoConfig, clock)
	q := &fakeQuoter{}
	s := NewService(chain, q, clock, "http://localhost:8080", true)
	if err := Seed(s, chain); err != nil {
		t.Fatal(err)
	}
	return s, chain, q, &now
}

func TestSeedShapes(t *testing.T) {
	s, _, _, _ := demo(t)
	want := map[string]Phase{
		"zk-reading-club": PhaseClosed, "protocol-tea": PhaseClosed,
		"rooftop-demos": PhaseDoors, "builders-night": PhaseOpen, "founders-dinner": PhasePriority,
	}
	for id, ph := range want {
		v, err := s.View(id)
		if err != nil {
			t.Fatalf("%s: %v", id, err)
		}
		if v.Phase != ph {
			t.Errorf("%s: phase %s, want %s", id, v.Phase, ph)
		}
	}
	zk, _ := s.View("zk-reading-club")
	if zk.Going != 14 || zk.Shows != 11 {
		t.Fatalf("zk: %d going %d shows", zk.Going, zk.Shows)
	}
	// Everyone who showed was paid out of the DAO; what stays is the dust.
	// Both Buenos Aires events share one community DAO.
	n := 0
	for _, p := range zk.Treasury.Proposals {
		if strings.Contains(p.Description, "zk-reading-club") && p.Status == "Approved" {
			n++
		}
	}
	if n != 11 {
		t.Fatalf("zk proposals: %d", n)
	}
	tea, _ := s.View("protocol-tea")
	if zk.Treasury.Balance != zk.Split.Dust+tea.Split.Dust {
		t.Fatalf("DAO left %d, dust %d+%d", zk.Treasury.Balance, zk.Split.Dust, tea.Split.Dust)
	}
	tk, err := s.Ticket("zk-reading-club", TicketShowedUp)
	if err != nil || tk.RSVP.PayStatus != core.PayPaid || tk.RSVP.Payout != zk.Split.PayoutPerShow {
		t.Fatalf("showed-up ticket: %v %+v", err, tk)
	}
	fl, _ := s.Ticket("zk-reading-club", TicketFlaked)
	if fl.RSVP.PayStatus != core.PayForfeit {
		t.Fatalf("flaked ticket: %+v", fl.RSVP)
	}
	if s.Chain.Shows(DemoRegular) != 2 {
		t.Fatalf("regular shows: %d", s.Chain.Shows(DemoRegular))
	}
}

func TestFullFlow(t *testing.T) {
	s, _, q, now := demo(t)
	ctx := context.Background()

	// Bad input is refused with a readable error.
	if _, err := s.StartRSVP(ctx, RSVPInput{Event: "builders-night", Name: "", Origin: "usdc-base", Address: "0x1"}); !errors.Is(err, ErrName) {
		t.Fatalf("name: %v", err)
	}
	if _, err := s.StartRSVP(ctx, RSVPInput{Event: "builders-night", Name: "Ada", Origin: "usdc-base", Address: "nope"}); !errors.Is(err, ErrAddress) {
		t.Fatalf("address: %v", err)
	}

	// RSVP from Base: a dry quote for exactly the deposit, to the relayer.
	g, err := s.StartRSVP(ctx, RSVPInput{Event: "builders-night", Name: "Ada", Origin: "usdc-base", Address: "0x1111111111111111111111111111111111111111"})
	if err != nil {
		t.Fatal(err)
	}
	if !q.last.Dry || q.last.Amount != "10000000" || q.last.Recipient == "" || q.last.SwapType != "EXACT_OUTPUT" {
		t.Fatalf("quote request: %+v", q.last)
	}
	if err := s.Settle(g.Ticket); err != nil {
		t.Fatal(err)
	}
	v, _ := s.View("builders-night")
	if v.Going != 24 {
		t.Fatalf("going: %d", v.Going)
	}
	// Same person again is refused.
	if _, err := s.StartRSVP(ctx, RSVPInput{Event: "builders-night", Name: "Ada again", Origin: "usdc-base", Address: "0x1111111111111111111111111111111111111111"}); !errors.Is(err, core.ErrDuplicateGuest) {
		t.Fatalf("dup: %v", err)
	}

	// Priority event: a newcomer waits, the regular gets in.
	if _, err := s.StartRSVP(ctx, RSVPInput{Event: "founders-dinner", Name: "New", Origin: "near", Address: "newcomer.testnet"}); !errors.Is(err, core.ErrPriorityOnly) {
		t.Fatalf("priority: %v", err)
	}
	reg, err := s.StartRSVP(ctx, RSVPInput{Event: "founders-dinner", Name: "Regular", Origin: "near", Address: DemoRegular})
	if err != nil || s.Settle(reg.Ticket) != nil {
		t.Fatalf("regular: %v", err)
	}

	// Jump to the event: door phone checks Ada in.
	*now = time.UnixMilli(int64(v.Ev.StartsAtMs)).Add(10 * time.Minute)
	door := DemoDoorKey()
	res := s.CheckIn(checkin.Sign(door, "builders-night", g.Ticket, *now))
	if !res.OK || res.Name != "Ada" {
		t.Fatalf("check-in: %+v", res)
	}
	if again := s.CheckIn(checkin.Sign(door, "builders-night", g.Ticket, *now)); again.OK {
		t.Fatal("double check-in accepted")
	}
	if stranger := s.CheckIn(checkin.Sign(door, "builders-night", "ZZZZZZZZZZZZZZZZ", *now)); stranger.OK {
		t.Fatal("stranger let in")
	}

	// Host closes; payouts run; Ada gets 10 + share of 23 flakes.
	n, err := s.CloseAndPay(DemoHost, "builders-night")
	if err != nil || n != 1 {
		t.Fatalf("close: %d %v", n, err)
	}
	tk, _ := s.Ticket("builders-night", g.Ticket)
	if tk.RSVP.PayStatus != core.PayPaid || tk.RSVP.Payout != 10_000_000+230_000_000 {
		t.Fatalf("payout: %+v", tk.RSVP)
	}
}

func TestCreateEvent(t *testing.T) {
	s, _, _, now := demo(t)
	v, err := s.Create(CreateInput{Host: DemoHost, Title: "Tuesday Hack Night!", City: "Porto", Deposit: "5", Starts: now.Add(48 * time.Hour), Capacity: 30, DAO: "porto.sputnikv2.testnet"})
	if err != nil {
		t.Fatal(err)
	}
	if v.Ev.Deposit != 5_000_000 || v.Phase != PhaseOpen || v.Meta.City != "Porto" {
		t.Fatalf("%+v", v.Ev)
	}
	if _, err := s.Create(CreateInput{Host: DemoHost, Title: "x", Deposit: "0", Starts: now.Add(time.Hour), Capacity: 3, DAO: "a.testnet"}); err == nil {
		t.Fatal("zero deposit accepted")
	}
}

func TestAuraRecomputesFromHistory(t *testing.T) {
	s, _, _, _ := demo(t)
	a := s.Aura(DemoRegular)
	if a.Shows != 2 || !a.Matches || a.RSVPs != 2 {
		t.Fatalf("%+v", a)
	}
	f := s.Aura("base:0x00000000000000000000000000000000000d3e70")
	if f.Shows != 0 || f.NoShows != 1 || !f.Matches {
		t.Fatalf("%+v", f)
	}
}
