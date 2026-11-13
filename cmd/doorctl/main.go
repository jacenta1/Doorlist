// Command doorctl runs and checks a real Doorlist event on NEAR testnet.
//
//	go run ./cmd/doorctl verify            # keyless: re-read the testnet event and check every number
//	go run ./cmd/doorctl run               # needs the master key in ~/.near-credentials (testnet only)
//
// "run" is resumable: each step first looks at the chain and skips itself
// when already done. Keys for the accounts it creates are written to
// ~/.near-credentials/testnet/, never into the repository.
package main

import (
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/base32"
	"encoding/json"
	"fmt"
	"math/big"
	"os"
	"path/filepath"
	"strings"
	"time"

	"doorlist/contract/core"
	"doorlist/internal/checkin"
	"doorlist/internal/near"
	"doorlist/internal/pot"
)

const (
	rpcURL   = "https://rpc.testnet.fastnear.com"
	master   = "doorlist-hk1113.testnet"
	contract = "dl." + master
	token    = "usdc.fakes.testnet" // a testnet stand-in for USDC: 6 decimals, open mint, worthless
	factory  = "sputnikv2.testnet"
	daoName  = "doorlist-hk1113"
	dao      = daoName + "." + factory
	eventID  = "testnet-night-1"
	deposit  = uint64(5_000_000) // 5 test-USDC
	runFile  = "docs/testnet/run.json"
)

var guests = []string{"ana", "bo", "cy", "di", "eli"}

func guestAcc(g string) string { return g + "." + master }

// Only the first three come to the door.
func shows(g string) bool { return g == "ana" || g == "bo" || g == "cy" }

func ticketFor(g string) string {
	h := sha256.Sum256([]byte(eventID + "/" + g))
	return base32.StdEncoding.EncodeToString(h[:10])
}

// Run is what gets committed: public facts only.
type Run struct {
	Network  string            `json:"network"`
	Contract string            `json:"contract"`
	Token    string            `json:"token"`
	DAO      string            `json:"dao"`
	Event    string            `json:"event"`
	Guests   map[string]string `json:"guests"` // account → ticket
	Steps    []Step            `json:"steps"`
}

// Step is one transaction.
type Step struct {
	Name string `json:"name"`
	Tx   string `json:"tx"`
	At   string `json:"at"`
}

var rpc = near.NewRPC(rpcURL)

func main() {
	if len(os.Args) < 2 {
		fmt.Println("usage: doorctl verify | run")
		os.Exit(2)
	}
	switch os.Args[1] {
	case "verify":
		os.Exit(verify())
	case "run":
		if err := run(); err != nil {
			fmt.Println("error:", err)
			os.Exit(1)
		}
	default:
		fmt.Println("usage: doorctl verify | run")
		os.Exit(2)
	}
}

// ---- keys ----

func credPath(acc string) string {
	h, _ := os.UserHomeDir()
	return filepath.Join(h, ".near-credentials", "testnet", acc+".json")
}

func loadKey(acc string) (ed25519.PrivateKey, error) {
	b, err := os.ReadFile(credPath(acc))
	if err != nil {
		return nil, err
	}
	var c struct {
		PrivateKey string `json:"private_key"`
		SecretKey  string `json:"secret_key"`
	}
	if err := json.Unmarshal(b, &c); err != nil {
		return nil, err
	}
	if c.PrivateKey == "" {
		c.PrivateKey = c.SecretKey
	}
	return near.ParsePrivateKey(c.PrivateKey)
}

func newKey(acc string) (ed25519.PrivateKey, error) {
	if k, err := loadKey(acc); err == nil {
		return k, nil
	}
	_, priv, _ := ed25519.GenerateKey(nil)
	pub := priv.Public().(ed25519.PublicKey)
	b, _ := json.Marshal(map[string]string{
		"account_id": acc, "public_key": near.PublicKeyString(pub), "private_key": "ed25519:" + core.Base58Encode(priv),
	})
	return priv, os.WriteFile(credPath(acc), b, 0o600)
}

func signer(acc string) *near.Signer {
	k, err := loadKey(acc)
	if err != nil {
		panic(fmt.Sprintf("no key for %s in %s: %v", acc, credPath(acc), err))
	}
	return &near.Signer{RPC: rpc, Account: acc, Key: k}
}

// ---- chain helpers ----

func ctx() (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.Background(), 90*time.Second)
}

func exists(acc string) bool {
	x, c := ctx()
	defer c()
	return rpc.Call(x, "query", map[string]any{"request_type": "view_account", "finality": "final", "account_id": acc}, nil) == nil
}

func codeHash(acc string) string {
	x, c := ctx()
	defer c()
	var r struct {
		CodeHash string `json:"code_hash"`
	}
	_ = rpc.Call(x, "query", map[string]any{"request_type": "view_account", "finality": "final", "account_id": acc}, &r)
	return r.CodeHash
}

func view(acc, method string, args any, out any) error {
	x, c := ctx()
	defer c()
	return rpc.View(x, acc, method, args, out)
}

func near_(s string) *big.Int { // "2.5" NEAR → yocto
	r, _ := new(big.Rat).SetString(s)
	r.Mul(r, new(big.Rat).SetInt(new(big.Int).Exp(big.NewInt(10), big.NewInt(24), nil)))
	return new(big.Int).Div(r.Num(), r.Denom())
}

func fc(method string, args any, tgas uint64, yocto string) near.FunctionCall {
	b, _ := json.Marshal(args)
	return near.FunctionCall{Method: method, Args: b, Gas: tgas * 1_000_000_000_000, Deposit: near.ParseYocto(yocto)}
}

func loadRun() *Run {
	r := &Run{Network: "testnet", Contract: contract, Token: token, DAO: dao, Event: eventID, Guests: map[string]string{}}
	if b, err := os.ReadFile(runFile); err == nil {
		_ = json.Unmarshal(b, r)
	}
	for _, g := range guests {
		r.Guests[guestAcc(g)] = ticketFor(g)
	}
	return r
}

func (r *Run) save() {
	_ = os.MkdirAll(filepath.Dir(runFile), 0o755)
	b, _ := json.MarshalIndent(r, "", "  ")
	_ = os.WriteFile(runFile, append(b, '\n'), 0o644)
}

func (r *Run) done(name string) bool {
	for _, s := range r.Steps {
		if s.Name == name {
			return true
		}
	}
	return false
}

func (r *Run) send(name string, s *near.Signer, receiver string, acts ...near.Action) error {
	if r.done(name) {
		return nil
	}
	x, c := ctx()
	defer c()
	out, err := s.SendActions(x, receiver, acts...)
	if err != nil {
		return fmt.Errorf("%s: %w", name, err)
	}
	if len(out.ReceiptFailures) > 0 {
		fmt.Printf("  note: %s had failed receipts: %.300s\n", name, strings.Join(out.ReceiptFailures, " | "))
	}
	for _, l := range out.Logs {
		fmt.Printf("  log: %.200s\n", l)
	}
	r.Steps = append(r.Steps, Step{Name: name, Tx: out.Hash, At: time.Now().UTC().Format(time.RFC3339)})
	r.save()
	fmt.Printf("✓ %-34s https://testnet.nearblocks.io/txns/%s\n", name, out.Hash)
	return nil
}

// ---- the run ----

func run() error {
	r := loadRun()
	m := signer(master)

	// 1. Accounts.
	wasm, err := os.ReadFile("contract/rust/target/near/doorlist_contract.wasm")
	if err != nil {
		return fmt.Errorf("build the contract first: make wasm (%w)", err)
	}
	if !exists(contract) {
		k, err := newKey(contract)
		if err != nil {
			return err
		}
		if err := r.send("create contract account", m, contract, near.CreateAccount{}, near.Transfer{Deposit: near_("2.45")}, near.AddFullAccessKey{PublicKey: k.Public().(ed25519.PublicKey)}); err != nil {
			return err
		}
	}
	for _, g := range guests {
		a := guestAcc(g)
		if exists(a) {
			continue
		}
		k, err := newKey(a)
		if err != nil {
			return err
		}
		if err := r.send("create guest "+a, m, a, near.CreateAccount{}, near.Transfer{Deposit: near_("0.06")}, near.AddFullAccessKey{PublicKey: k.Public().(ed25519.PublicKey)}); err != nil {
			return err
		}
	}

	// 2. Deploy and init.
	if codeHash(contract) == "11111111111111111111111111111111" {
		if err := r.send("deploy + init contract", signer(contract), contract, near.DeployContract{Code: wasm},
			fc("init", map[string]string{"owner": master, "token": token, "relayer": master}, 30, "0")); err != nil {
			return err
		}
	}

	// 3. The DAO that holds the pot: hosts are the council, the contract has a transfer-only role.
	if !exists(dao) {
		args := pot.CreateDAOArgs(daoName, "Holds Doorlist event deposits (testnet)", []string{master}, contract)
		if err := r.send("create Sputnik DAO", m, factory, near.FunctionCall{Method: "create", Args: args, Gas: 200_000_000_000_000, Deposit: near_("5.42")}); err != nil {
			return err
		}
	}

	// 4. Token storage for everyone who will hold test-USDC.
	holders := append([]string{contract, dao}, func() []string {
		out := []string{}
		for _, g := range guests {
			out = append(out, guestAcc(g))
		}
		return out
	}()...)
	for _, h := range holders {
		var bal *struct {
			Total string `json:"total"`
		}
		_ = view(token, "storage_balance_of", map[string]string{"account_id": h}, &bal)
		if bal != nil {
			continue
		}
		if err := r.send("token storage "+h, m, token, fc("storage_deposit", map[string]any{"account_id": h, "registration_only": true}, 30, "1250000000000000000000")); err != nil {
			return err
		}
	}

	// 5. Test-USDC for the guests (open mint on this testnet token).
	for _, g := range guests {
		a := guestAcc(g)
		var b string
		_ = view(token, "ft_balance_of", map[string]string{"account_id": a}, &b)
		if b != "0" && b != "" {
			continue
		}
		if err := r.send("mint 10 test-USDC to "+a, m, token, fc("mint", map[string]string{"account_id": a, "amount": "10000000"}, 30, "0")); err != nil {
			return err
		}
	}

	// 6. Host creates the event: RSVPs close in 20 minutes; doors are already open.
	var ev *core.Event
	_ = view(contract, "get_event", map[string]string{"event": eventID}, &ev)
	if ev == nil {
		start := uint64(time.Now().Add(20 * time.Minute).UnixMilli())
		in := core.CreateEventInput{ID: eventID, Title: "Doorlist testnet night #1", Deposit: pot.Itoa(deposit),
			StartsAtMs: start, DoorsOpenMs: start - 2*3600_000, CheckinCloseMs: start + 3*3600_000, Capacity: 10, DAO: dao}
		if err := r.send("host creates event", m, contract, fc("create_event", map[string]any{"input": in}, 30, "0")); err != nil {
			return err
		}
	}

	// 7. Host registers a door phone's key (kept outside the repo).
	door, err := newKey("door-" + eventID)
	if err != nil {
		return err
	}
	doorPub := core.FormatEd25519Key(door.Public().(ed25519.PublicKey))
	if err := r.send("host registers door key", m, contract, fc("add_door_key", map[string]string{"event": eventID, "key": doorPub}, 30, "0")); err != nil {
		return err
	}

	// 8. Guests RSVP with a deposit, straight from their own accounts.
	for _, g := range guests {
		if err := r.send("gas top-up "+guestAcc(g), m, guestAcc(g), near.Transfer{Deposit: near_("0.08")}); err != nil {
			return err
		}
	}
	for _, g := range guests {
		a := guestAcc(g)
		msg, _ := json.Marshal(core.DepositMsg{Event: eventID, Ticket: ticketFor(g)})
		if err := r.send("RSVP "+a, signer(a), token, fc("ft_transfer_call", map[string]string{"receiver_id": contract, "amount": pot.Itoa(deposit), "msg": string(msg)}, 100, "1")); err != nil {
			return err
		}
	}

	// 9. The door checks in three of five; the relayer submits the signed scans.
	for _, g := range guests {
		if !shows(g) {
			continue
		}
		s := checkin.Sign(door, eventID, ticketFor(g), time.Now())
		if err := r.send("door checks in "+guestAcc(g), m, contract, fc("check_in", map[string]any{"input": s.Input()}, 30, "0")); err != nil {
			return err
		}
	}

	// 10. Host closes; payouts go through the DAO, two per call.
	if err := r.send("host closes event", m, contract, fc("close_event", map[string]string{"event": eventID}, 30, "0")); err != nil {
		return err
	}
	for i := 1; i <= 3; i++ {
		var rs []core.RSVP
		_ = view(contract, "list_rsvps", map[string]any{"event": eventID, "from": 0, "limit": 50}, &rs)
		owed := 0
		for _, x := range rs {
			if x.PayStatus == core.PayOwed {
				owed++
			}
		}
		if owed == 0 {
			break
		}
		if err := r.send(fmt.Sprintf("payout batch %d", i), m, contract, fc("payout_batch", map[string]any{"event": eventID, "limit": 2}, 300, "0")); err != nil {
			return err
		}
	}
	fmt.Println("\nrun complete; now: go run ./cmd/doorctl verify")
	return nil
}

// ---- verify (keyless) ----

func verify() int {
	fail := 0
	check := func(ok bool, what string, args ...any) {
		mark := "✓"
		if !ok {
			mark = "✗"
			fail++
		}
		fmt.Printf("%s %s\n", mark, fmt.Sprintf(what, args...))
	}
	fmt.Printf("Doorlist on NEAR testnet · read live from %s · no keys used\n\n", rpcURL)
	var ev *core.Event
	if err := view(contract, "get_event", map[string]string{"event": eventID}, &ev); err != nil || ev == nil {
		fmt.Println("✗ cannot read event:", err)
		return 1
	}
	var rs []core.RSVP
	_ = view(contract, "list_rsvps", map[string]any{"event": eventID, "from": 0, "limit": 50}, &rs)
	fmt.Printf("event   %s · \"%s\" · host %s · status %s\n", ev.ID, ev.Title, ev.Host, ev.Status)
	fmt.Printf("pot     %s test-USDC from %d RSVPs · %d checked in · DAO %s\n\n", pot.USDC(ev.Pot), ev.Going, ev.Shows, ev.DAO)

	s := core.Split(ev.Deposit, ev.Going, ev.Shows)
	shows := 0
	for _, r := range rs {
		bal := "?"
		_ = view(token, "ft_balance_of", map[string]string{"account_id": r.PayTo}, &bal)
		state := "no-show"
		if r.CheckedInAt != 0 {
			shows++
			state = "checked in " + time.UnixMilli(int64(r.CheckedInAt)).UTC().Format("15:04:05")
		}
		pid := "-"
		if r.ProposalID != nil {
			pid = fmt.Sprintf("#%d", *r.ProposalID)
		}
		fmt.Printf("  %-26s %-24s payout %-10s %-9s proposal %-4s wallet now %s\n", r.Guest, state, pot.USDC(r.Payout), r.PayStatus, pid, pot.USDC(atou(bal)))
		if r.CheckedInAt != 0 {
			check(r.Payout == s.PayoutPerShow && r.PayStatus == core.PayPaid, "%s paid deposit + share = %s", r.Guest, pot.USDC(s.PayoutPerShow))
		} else {
			check(r.Payout == 0 && r.PayStatus == core.PayForfeit, "%s forfeited their deposit", r.Guest)
		}
	}
	check(shows == int(ev.Shows), "check-in count matches the RSVP records (%d)", shows)
	var daoBal string
	_ = view(token, "ft_balance_of", map[string]string{"account_id": ev.DAO}, &daoBal)
	check(atou(daoBal) == s.ToDAO, "DAO keeps only the rounding dust: %s test-USDC", pot.USDC(atou(daoBal)))
	var cBal string
	_ = view(token, "ft_balance_of", map[string]string{"account_id": contract}, &cBal)
	check(atou(cBal) == 0, "the Doorlist contract itself holds no deposits (%s)", pot.USDC(atou(cBal)))

	var props []struct {
		ID     uint64 `json:"id"`
		Status string `json:"status"`
		Kind   struct {
			Transfer *struct {
				Receiver string `json:"receiver_id"`
				Amount   string `json:"amount"`
			} `json:"Transfer"`
		} `json:"kind"`
		Description string `json:"description"`
	}
	_ = view(ev.DAO, "get_proposals", map[string]any{"from_index": 0, "limit": 50}, &props)
	approved := 0
	for _, p := range props {
		if p.Kind.Transfer != nil && strings.Contains(p.Description, eventID) && p.Status == "Approved" {
			approved++
		}
	}
	check(approved == int(ev.Shows), "DAO approved one Transfer proposal per attendee (%d)", approved)
	for _, r := range rs {
		check(chainShows(r.Guest) >= boolInt(r.CheckedInAt != 0), "reputation for %s counts this check-in", r.Guest)
	}
	if fail > 0 {
		fmt.Printf("\n%d check(s) failed\n", fail)
		return 1
	}
	fmt.Println("\nall checks passed")
	return 0
}

func chainShows(g string) int {
	var n int
	_ = view(contract, "get_shows", map[string]string{"account": g}, &n)
	return n
}

func boolInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

func atou(s string) uint64 {
	var v uint64
	fmt.Sscan(strings.Trim(s, `"`), &v)
	return v
}
