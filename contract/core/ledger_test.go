package core

import (
	"crypto/ed25519"
	"encoding/json"
	"errors"
	"os"
	"strings"
	"testing"
)

type testEnv struct {
	now    uint64
	caller string
}

func (e *testEnv) NowMs() uint64       { return e.now }
func (e *testEnv) Predecessor() string { return e.caller }
func (e *testEnv) VerifyEd25519(pub [32]byte, msg []byte, sig [64]byte) bool {
	return ed25519.Verify(pub[:], msg, sig[:])
}

const (
	usdc    = "usdc.fakes.testnet"
	relayer = "relay.doorlist.testnet"
	host    = "host.testnet"
	dao     = "builders.sputnik-dao.near"
	hour    = uint64(3600_000)
	t0      = uint64(1_800_000_000_000)
)

func setup(t *testing.T) (*Ledger, *testEnv) {
	t.Helper()
	env := &testEnv{now: t0, caller: "owner.testnet"}
	l := New(NewMemKV(), env)
	if err := l.Init(Config{Owner: "owner.testnet", Token: usdc, Relayer: relayer}); err != nil {
		t.Fatal(err)
	}
	env.caller = host
	_, err := l.CreateEvent(CreateEventInput{
		ID: "builders-night", Title: "Builders Night", Deposit: "10000000",
		StartsAtMs: t0 + 48*hour, CheckinCloseMs: t0 + 51*hour,
		PublicFromMs: t0 + 24*hour, PriorityMinShows: 2,
		Capacity: 3, DAO: dao,
	})
	if err != nil {
		t.Fatal(err)
	}
	return l, env
}

func deposit(t *testing.T, l *Ledger, env *testEnv, sender, guest, ticket string, amount uint64) (uint64, []Call, error) {
	t.Helper()
	env.caller = usdc
	msg, _ := json.Marshal(DepositMsg{Event: "builders-night", Ticket: ticket, Guest: guest})
	return l.OnDeposit(sender, amount, string(msg))
}

func doorKey(t *testing.T) (ed25519.PrivateKey, string) {
	t.Helper()
	pub, priv, _ := ed25519.GenerateKey(nil)
	return priv, FormatEd25519Key(pub)
}

func signIn(priv ed25519.PrivateKey, key, ticket string, at uint64) CheckInInput {
	sig := ed25519.Sign(priv, CheckinMessage("builders-night", ticket, at))
	return CheckInInput{Event: "builders-night", Ticket: ticket, AtMs: at, DoorKey: key, Signature: Base58Encode(sig)}
}

func TestInitOnce(t *testing.T) {
	l, _ := setup(t)
	if err := l.Init(Config{Owner: "a.near", Token: "b.near", Relayer: "c.near"}); !errors.Is(err, ErrExists) {
		t.Fatalf("second init: %v", err)
	}
}

func TestCreateEventValidation(t *testing.T) {
	l, env := setup(t)
	env.caller = host
	bad := []CreateEventInput{
		{ID: "x", Title: "t", Deposit: "1", StartsAtMs: t0 + hour, CheckinCloseMs: t0 + 2*hour, Capacity: 1, DAO: dao},
		{ID: "ok-id", Title: "t", Deposit: "0", StartsAtMs: t0 + hour, CheckinCloseMs: t0 + 2*hour, Capacity: 1, DAO: dao},
		{ID: "ok-id", Title: "t", Deposit: "1", StartsAtMs: t0 - 1, CheckinCloseMs: t0 + 2*hour, Capacity: 1, DAO: dao},
		{ID: "ok-id", Title: "t", Deposit: "1", StartsAtMs: t0 + hour, CheckinCloseMs: t0 + hour, Capacity: 1, DAO: dao},
		{ID: "ok-id", Title: "t", Deposit: "1", StartsAtMs: t0 + hour, CheckinCloseMs: t0 + 2*hour, Capacity: 0, DAO: dao},
		{ID: "ok-id", Title: "t", Deposit: "1", StartsAtMs: t0 + hour, CheckinCloseMs: t0 + 2*hour, Capacity: 1, DAO: "NOT VALID"},
	}
	for i, in := range bad {
		if _, err := l.CreateEvent(in); !errors.Is(err, ErrBadInput) {
			t.Errorf("case %d: want ErrBadInput, got %v", i, err)
		}
	}
	_, err := l.CreateEvent(CreateEventInput{ID: "builders-night", Title: "dup", Deposit: "1", StartsAtMs: t0 + hour, CheckinCloseMs: t0 + 2*hour, Capacity: 1, DAO: dao})
	if !errors.Is(err, ErrExists) {
		t.Fatalf("duplicate id: %v", err)
	}
	ev, _ := l.Event("builders-night")
	if ev.DoorsOpenMs != ev.StartsAtMs-hour {
		t.Fatalf("doors open default: %d", ev.DoorsOpenMs)
	}
}

func TestDepositForwardsToDAO(t *testing.T) {
	l, env := setup(t)
	env.now = t0 + 25*hour // public RSVPs open
	refund, calls, err := deposit(t, l, env, relayer, "ana.near", "AAAABBBB", 10_000_000)
	if err != nil || refund != 0 {
		t.Fatalf("deposit: refund=%d err=%v", refund, err)
	}
	if len(calls) != 1 || calls[0].Receiver != usdc || calls[0].Method != "ft_transfer" || calls[0].Deposit != "1" {
		t.Fatalf("calls: %+v", calls)
	}
	var args map[string]string
	_ = json.Unmarshal(calls[0].Args, &args)
	if args["receiver_id"] != dao || args["amount"] != "10000000" {
		t.Fatalf("args: %v", args)
	}
	ev, _ := l.Event("builders-night")
	if ev.Going != 1 || ev.Pot != 10_000_000 {
		t.Fatalf("event: %+v", ev)
	}

	// A guest who paid from Base: identity is chain:address, payout goes to the relayer's outbox.
	env.caller = usdc
	msg := `{"event":"builders-night","ticket":"BBBBCCCC","guest":"base:0x11aa22bb33cc44dd55ee66ff77889900aabbccdd","pay_to":"outbox.doorlist.testnet"}`
	if refund, _, err := l.OnDeposit(relayer, 10_000_000, msg); err != nil || refund != 0 {
		t.Fatalf("cross-chain deposit: %v", err)
	}
	r, _ := l.RSVP("builders-night", "BBBBCCCC")
	if r.Guest != "base:0x11aa22bb33cc44dd55ee66ff77889900aabbccdd" || r.PayTo != "outbox.doorlist.testnet" {
		t.Fatalf("rsvp: %+v", r)
	}
	// Nobody but the relayer can redirect a payout.
	if _, _, err := l.OnDeposit("eve.near", 10_000_000, `{"event":"builders-night","ticket":"CCCCDDDD","pay_to":"thief.near"}`); !errors.Is(err, ErrUnauthorized) {
		t.Fatalf("redirect: %v", err)
	}
}

func TestDepositRejections(t *testing.T) {
	l, env := setup(t)
	env.now = t0 + 25*hour
	cases := []struct {
		name          string
		sender, guest string
		ticket        string
		amount        uint64
		want          error
	}{
		{"wrong amount", relayer, "ana.near", "AAAABBBB", 9_000_000, ErrWrongAmount},
		{"bad ticket", relayer, "ana.near", "lowercase", 10_000_000, ErrBadInput},
		{"stranger pays for someone else", "mallory.near", "ana.near", "AAAABBBB", 10_000_000, ErrUnauthorized},
	}
	for _, c := range cases {
		refund, _, err := deposit(t, l, env, c.sender, c.guest, c.ticket, c.amount)
		if !errors.Is(err, c.want) || refund != c.amount {
			t.Errorf("%s: err=%v refund=%d", c.name, err, refund)
		}
	}
	// wrong token contract
	env.caller = "fake-usdc.near"
	if refund, _, err := l.OnDeposit("ana.near", 10_000_000, `{"event":"builders-night","ticket":"AAAABBBB"}`); !errors.Is(err, ErrUnauthorized) || refund != 10_000_000 {
		t.Fatalf("wrong token: %v", err)
	}
	// duplicate guest, then full
	if _, _, err := deposit(t, l, env, "ana.near", "", "AAAABBBB", 10_000_000); err != nil {
		t.Fatal(err)
	}
	if _, _, err := deposit(t, l, env, relayer, "ana.near", "AAAACCCC", 10_000_000); !errors.Is(err, ErrDuplicateGuest) {
		t.Fatalf("dup guest: %v", err)
	}
	if _, _, err := deposit(t, l, env, relayer, "ana.near", "AAAABBBB", 10_000_000); !errors.Is(err, ErrDuplicateGuest) {
		t.Fatalf("dup ticket: %v", err)
	}
	deposit(t, l, env, relayer, "bo.near", "AAAADDDD", 10_000_000)
	deposit(t, l, env, relayer, "cy.near", "AAAAEEEE", 10_000_000)
	if _, _, err := deposit(t, l, env, relayer, "di.near", "AAAAFFFF", 10_000_000); !errors.Is(err, ErrFull) {
		t.Fatalf("full: %v", err)
	}
	env.now = t0 + 48*hour
	if _, _, err := deposit(t, l, env, relayer, "di.near", "AAAAFFFF", 10_000_000); err == nil {
		t.Fatal("deposit after start accepted")
	}
}

func TestPriorityWindowNeedsShows(t *testing.T) {
	l, env := setup(t)
	env.now = t0 + hour // before public_from
	if _, _, err := deposit(t, l, env, relayer, "new.near", "AAAABBBB", 10_000_000); !errors.Is(err, ErrPriorityOnly) {
		t.Fatalf("newcomer in priority window: %v", err)
	}
	l.put(repKey("regular.near"), uint32(2))
	if _, _, err := deposit(t, l, env, relayer, "regular.near", "AAAABBBB", 10_000_000); err != nil {
		t.Fatalf("regular in priority window: %v", err)
	}
	if err := l.CanRSVP("builders-night", "new.near"); !errors.Is(err, ErrPriorityOnly) {
		t.Fatalf("CanRSVP: %v", err)
	}
}

func TestCheckInNeedsHostSignedDoorKey(t *testing.T) {
	l, env := setup(t)
	env.now = t0 + 25*hour
	deposit(t, l, env, relayer, "ana.near", "AAAABBBB", 10_000_000)
	priv, key := doorKey(t)

	// Only the host can register a door key.
	env.caller = "mallory.near"
	if err := l.AddDoorKey("builders-night", key); !errors.Is(err, ErrUnauthorized) {
		t.Fatalf("non-host add key: %v", err)
	}
	env.caller = host
	if err := l.AddDoorKey("builders-night", key); err != nil {
		t.Fatal(err)
	}

	env.caller = relayer
	// Too early: doors not open.
	if _, err := l.CheckIn(signIn(priv, key, "AAAABBBB", env.now)); !errors.Is(err, ErrOutsideWindow) {
		t.Fatalf("early: %v", err)
	}
	env.now = t0 + 47*hour + 30*60*1000 // doors open at 47h
	// Unregistered key.
	priv2, key2 := doorKey(t)
	if _, err := l.CheckIn(signIn(priv2, key2, "AAAABBBB", env.now)); !errors.Is(err, ErrUnknownDoorKey) {
		t.Fatalf("unknown key: %v", err)
	}
	// Tampered: signature for another ticket.
	in := signIn(priv, key, "AAAACCCC", env.now)
	in.Ticket = "AAAABBBB"
	if _, err := l.CheckIn(in); !errors.Is(err, ErrBadSignature) {
		t.Fatalf("tampered: %v", err)
	}
	// Stale timestamp.
	if _, err := l.CheckIn(signIn(priv, key, "AAAABBBB", env.now-MaxClockSkewMs-1)); !errors.Is(err, ErrOutsideWindow) {
		t.Fatalf("stale: %v", err)
	}
	// Not on the list.
	if _, err := l.CheckIn(signIn(priv, key, "ZZZZZZZZ", env.now)); !errors.Is(err, ErrNotFound) {
		t.Fatalf("not on list: %v", err)
	}
	r, err := l.CheckIn(signIn(priv, key, "AAAABBBB", env.now))
	if err != nil || r.CheckedInAt != env.now {
		t.Fatalf("check in: %v", err)
	}
	if _, err := l.CheckIn(signIn(priv, key, "AAAABBBB", env.now+1)); !errors.Is(err, ErrAlreadyIn) {
		t.Fatalf("twice: %v", err)
	}
	if l.Shows("ana.near") != 1 {
		t.Fatal("reputation not counted")
	}
	// Revoked key stops working.
	env.caller = host
	_ = l.RemoveDoorKey("builders-night", key)
	if _, err := l.CheckIn(signIn(priv, key, "AAAABBBB", env.now)); !errors.Is(err, ErrUnknownDoorKey) {
		t.Fatalf("revoked: %v", err)
	}
}

func TestCloseSplitAndPayouts(t *testing.T) {
	l, env := setup(t)
	env.now = t0 + 25*hour
	deposit(t, l, env, relayer, "ana.near", "AAAABBBB", 10_000_000)
	deposit(t, l, env, relayer, "bo.near", "AAAACCCC", 10_000_000)
	deposit(t, l, env, "cy.near", "", "AAAADDDD", 10_000_000)
	priv, key := doorKey(t)
	env.caller = host
	_ = l.AddDoorKey("builders-night", key)
	env.now = t0 + 48*hour
	env.caller = relayer
	l.CheckIn(signIn(priv, key, "AAAABBBB", env.now))
	l.CheckIn(signIn(priv, key, "AAAADDDD", env.now))

	// Non-host cannot close during the window.
	if _, err := l.Close("builders-night"); !errors.Is(err, ErrTooEarly) {
		t.Fatalf("early close: %v", err)
	}
	// Payouts need a closed event.
	if _, err := l.PayoutBatch("builders-night", 5); !errors.Is(err, ErrTooEarly) {
		t.Fatalf("payout before close: %v", err)
	}
	env.caller = host
	ev, err := l.Close("builders-night")
	if err != nil {
		t.Fatal(err)
	}
	if ev.SharePerShow != 5_000_000 || ev.Forfeit != 10_000_000 || ev.Dust != 0 {
		t.Fatalf("split: %+v", ev)
	}
	bo, _ := l.RSVP("builders-night", "AAAACCCC")
	if bo.PayStatus != PayForfeit || bo.Payout != 0 {
		t.Fatalf("no-show: %+v", bo)
	}
	// Check-in after close is refused.
	env.caller = relayer
	if _, err := l.CheckIn(signIn(priv, key, "AAAACCCC", env.now)); !errors.Is(err, ErrClosed) {
		t.Fatalf("after close: %v", err)
	}

	calls, err := l.PayoutBatch("builders-night", 10)
	if err != nil || len(calls) != 2 {
		t.Fatalf("batch: %v %d", err, len(calls))
	}
	c := calls[0]
	if c.Receiver != dao || c.Method != "add_proposal" || c.Then == nil || c.Then.Method != "on_payout_proposed" {
		t.Fatalf("proposal call: %+v", c)
	}
	if !strings.Contains(string(c.Args), `"receiver_id":"ana.near"`) || !strings.Contains(string(c.Args), `"amount":"15000000"`) || !strings.Contains(string(c.Args), `"token_id":"`+usdc+`"`) {
		t.Fatalf("proposal args: %s", c.Args)
	}
	// Second batch is empty: both are in flight.
	if again, _ := l.PayoutBatch("builders-night", 10); len(again) != 0 {
		t.Fatalf("double proposal: %d", len(again))
	}
	// A failed add_proposal goes back to owed.
	if calls, _ := l.OnPayoutProposed("builders-night", "AAAADDDD", false, 0); calls != nil {
		t.Fatal("failed proposal produced calls")
	}
	if r, _ := l.RSVP("builders-night", "AAAADDDD"); r.PayStatus != PayOwed {
		t.Fatalf("retry status: %s", r.PayStatus)
	}
	votes, err := l.OnPayoutProposed("builders-night", "AAAABBBB", true, 41)
	if err != nil || len(votes) != 1 || votes[0].Method != "act_proposal" || !strings.Contains(string(votes[0].Args), `"id":41`) || !strings.Contains(string(votes[0].Args), `"VoteApprove"`) {
		t.Fatalf("vote: %v %+v", err, votes)
	}
	if err := l.OnPayoutApproved("builders-night", "AAAABBBB", true); err != nil {
		t.Fatal(err)
	}
	ana, _ := l.RSVP("builders-night", "AAAABBBB")
	if ana.PayStatus != PayPaid || *ana.ProposalID != 41 {
		t.Fatalf("paid: %+v", ana)
	}
	// Callbacks cannot be replayed.
	if _, err := l.OnPayoutProposed("builders-night", "AAAABBBB", true, 99); !errors.Is(err, ErrBadInput) {
		t.Fatalf("replayed callback: %v", err)
	}
}

func TestAnyoneClosesAfterWindow(t *testing.T) {
	l, env := setup(t)
	env.now = t0 + 51*hour + 1
	env.caller = "anyone.near"
	ev, err := l.Close("builders-night")
	if err != nil || ev.Status != StatusClosed {
		t.Fatalf("close: %v", err)
	}
}

func TestCancelRefundsEveryone(t *testing.T) {
	l, env := setup(t)
	env.now = t0 + 25*hour
	deposit(t, l, env, relayer, "ana.near", "AAAABBBB", 10_000_000)
	env.caller = "mallory.near"
	if _, err := l.Cancel("builders-night"); !errors.Is(err, ErrUnauthorized) {
		t.Fatalf("non-host cancel: %v", err)
	}
	env.caller = host
	if _, err := l.Cancel("builders-night"); err != nil {
		t.Fatal(err)
	}
	r, _ := l.RSVP("builders-night", "AAAABBBB")
	if r.PayStatus != PayOwed || r.Payout != 10_000_000 {
		t.Fatalf("refund: %+v", r)
	}
}

func TestSplitVectors(t *testing.T) {
	raw, err := os.ReadFile("../../testdata/split-vectors.json")
	if err != nil {
		t.Fatal(err)
	}
	var vs []struct {
		Name          string `json:"name"`
		Deposit       uint64 `json:"deposit"`
		Going         uint32 `json:"going"`
		Shows         uint32 `json:"shows"`
		PayoutPerShow uint64 `json:"payout_per_show"`
		Forfeit       uint64 `json:"forfeit"`
		Dust          uint64 `json:"dust"`
		ToDAO         uint64 `json:"to_dao"`
	}
	if err := json.Unmarshal(raw, &vs); err != nil || len(vs) == 0 {
		t.Fatalf("vectors: %v", err)
	}
	for _, v := range vs {
		s := Split(v.Deposit, v.Going, v.Shows)
		if s.Forfeit != v.Forfeit || s.Dust != v.Dust || s.PayoutPerShow != v.PayoutPerShow || s.ToDAO != v.ToDAO {
			t.Errorf("%s: %+v", v.Name, s)
		}
	}
	// Conservation: payouts + to_dao == pot, for many shapes.
	for going := uint32(0); going < 60; going++ {
		for shows := uint32(0); shows <= going; shows++ {
			s := Split(7_777_777, going, shows)
			if s.PayoutPerShow*uint64(shows)+s.ToDAO != s.Pot {
				t.Fatalf("not conserved at %d/%d: %+v", shows, going, s)
			}
		}
	}
}

func TestBase58RoundTrip(t *testing.T) {
	for _, in := range [][]byte{{}, {0}, {0, 0, 1}, []byte("doorlist"), make([]byte, 32)} {
		out, err := Base58Decode(Base58Encode(in))
		if err != nil || string(out) != string(in) {
			t.Fatalf("%v -> %v (%v)", in, out, err)
		}
	}
	// Known vector: "hello world" in Bitcoin base58.
	if got := Base58Encode([]byte("hello world")); got != "StV1DL6CwTryKyV" {
		t.Fatalf("vector: %s", got)
	}
}
