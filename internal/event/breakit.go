package event

import (
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/json"
	"time"

	"doorlist/contract/core"
	"doorlist/internal/checkin"
)

// Attempt is one attack on the ledger and what the ledger said.
type Attempt struct {
	Name    string
	What    string
	Refused bool
	Reason  string
}

// TryToBreakIt runs a fixed list of attacks against a fresh ledger (the same
// contract core the demo runs) and reports which were refused. Nothing is
// mocked: each attack calls the real entry point with real signatures.
func TryToBreakIt() []Attempt {
	t0 := time.Date(2026, 11, 14, 12, 0, 0, 0, time.UTC)
	now := t0
	c := NewSimChain(DemoConfig, func() time.Time { return now })
	h := time.Hour
	const ev, host = "break-it", "host.testnet"
	_, _ = c.CreateEvent(host, core.CreateEventInput{
		ID: ev, Title: "Break it", Deposit: "10000000",
		StartsAtMs: uint64(t0.Add(48 * h).UnixMilli()), CheckinCloseMs: uint64(t0.Add(51 * h).UnixMilli()),
		PublicFromMs: uint64(t0.Add(24 * h).UnixMilli()), PriorityMinShows: 1, Capacity: 3, DAO: "club.sputnikv2.testnet",
	})
	seed := sha256.Sum256([]byte("break-it door"))
	door := ed25519.NewKeyFromSeed(seed[:])
	seed2 := sha256.Sum256([]byte("someone else's phone"))
	rogue := ed25519.NewKeyFromSeed(seed2[:])
	_ = c.AddDoorKey(host, ev, core.FormatEd25519Key(door.Public().(ed25519.PublicKey)))

	out := []Attempt{}
	try := func(name, what string, err error) {
		a := Attempt{Name: name, What: what, Refused: err != nil}
		if err != nil {
			a.Reason = err.Error()
		}
		out = append(out, a)
	}
	raw := func(caller, sender string, amount uint64, msg core.DepositMsg) error {
		c.mu.Lock()
		defer c.mu.Unlock()
		c.caller = caller
		m, _ := json.Marshal(msg)
		_, _, err := c.ledger().OnDeposit(sender, amount, string(m))
		return err
	}

	// RSVP attacks.
	try("Jump the regulars' queue", "A newcomer RSVPs during the regulars-only window.",
		c.Deposit(ev, "AAAAAAAA", "newcomer.testnet", "newcomer.testnet", 10_000_000))
	now = t0.Add(25 * h)
	try("Underpay the deposit", "RSVP with 1 USDC instead of 10.",
		raw(DemoConfig.Token, DemoConfig.Relayer, 1_000_000, core.DepositMsg{Event: ev, Ticket: "AAAAAAAA", Guest: "ana.testnet"}))
	try("Pay with a fake token", "A look-alike token contract calls ft_on_transfer.",
		raw("fake-usdc.testnet", "ana.testnet", 10_000_000, core.DepositMsg{Event: ev, Ticket: "AAAAAAAA"}))
	try("Redirect someone's payout", "A stranger deposits for a guest but names their own account as pay_to.",
		raw(DemoConfig.Token, "mallory.testnet", 10_000_000, core.DepositMsg{Event: ev, Ticket: "AAAAAAAA", Guest: "ana.testnet", PayTo: "mallory.testnet"}))
	_ = c.Deposit(ev, "AAAAAAAA", "ana.testnet", "ana.testnet", 10_000_000)
	_ = c.Deposit(ev, "BBBBBBBB", "bo.testnet", "bo.testnet", 10_000_000)
	try("RSVP twice", "The same guest deposits again to claim two shares.",
		c.Deposit(ev, "CCCCCCCC", "ana.testnet", "ana.testnet", 10_000_000))

	// Door attacks.
	checkIn := func(s checkin.Scan) error { _, err := c.CheckIn(s.Input()); return err }
	try("Check in before doors", "A valid door signature, a day early.",
		checkIn(checkin.Sign(door, ev, "AAAAAAAA", now)))
	now = t0.Add(48 * h)
	try("Fake check-in", "A phone the host never registered signs a check-in.",
		checkIn(checkin.Sign(rogue, ev, "BBBBBBBB", now)))
	try("Non-host adds a door", "A guest registers their own phone as a door.",
		c.AddDoorKey("bo.testnet", ev, core.FormatEd25519Key(rogue.Public().(ed25519.PublicKey))))
	forged := checkin.Sign(door, ev, "AAAAAAAA", now)
	forged.Ticket = "BBBBBBBB"
	try("Reuse a signature for another ticket", "Take the door's signature for one guest and swap in a different ticket.",
		checkIn(forged))
	try("Ticket that never paid", "A made-up ticket code with a valid door signature.",
		checkIn(checkin.Sign(door, ev, "ZZZZZZZZ", now)))
	good := checkin.Sign(door, ev, "AAAAAAAA", now)
	_ = checkIn(good)
	try("Replay a check-in", "Submit the same signed check-in a second time.", checkIn(good))
	try("Stale signature", "A check-in signed 20 minutes ago, held back and sent late.",
		checkIn(checkin.Sign(door, ev, "BBBBBBBB", now.Add(-20*time.Minute))))

	// Close and payout attacks.
	closeAs := func(who string) error { _, err := c.Close(who, ev); return err }
	payout := func() error {
		c.mu.Lock()
		defer c.mu.Unlock()
		_, err := c.ledger().PayoutBatch(ev, 10)
		return err
	}
	try("Pay out before close", "Ask for payouts while check-in is still open.", payout())
	try("Close early", "A guest closes the event mid-evening to cut out late arrivals.", closeAs("ana.testnet"))
	_ = closeAs(host)
	try("Check in after close", "The no-show gets a door signature after the event closed.",
		checkIn(checkin.Sign(door, ev, "BBBBBBBB", now)))
	try("Close twice", "Close again to recompute the split.", closeAs(host))
	_, _ = c.Payout(ev)
	callback := func() error {
		c.mu.Lock()
		defer c.mu.Unlock()
		_, err := c.ledger().OnPayoutProposed(ev, "AAAAAAAA", true, 999)
		return err
	}
	try("Double refund", "Replay the payout callback for a guest who was already paid.", callback())
	n, _ := c.Payout(ev)
	var again error
	if n == 0 {
		again = core.ErrBadInput
	}
	out = append(out, Attempt{Name: "Second payout round", What: "Run payouts again after everyone was paid.", Refused: n == 0, Reason: "nothing owed: 0 proposals filed"})
	_ = again
	try("No-show claims a payout", "The guest who didn't come replays a payout callback for their ticket.", func() error {
		c.mu.Lock()
		defer c.mu.Unlock()
		_, err := c.ledger().OnPayoutProposed(ev, "BBBBBBBB", true, 1000)
		return err
	}())
	return out
}
