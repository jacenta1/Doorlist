// Doorlist escrow contract, written with near-sdk-go.
//
// Build: `near-go build` (TinyGo underneath), see contract/README.md.
// All logic lives in ./core; this file only adapts the NEAR runtime to it.
//
// SDK note: the generated wrapper JSON-encodes a method's return value and
// then hands it to ReturnValue, which encodes strings a second time. NEP-141
// token contracts read ft_on_transfer's return as a bare U128 string, so a
// double-encoded "0" would make the token refund every deposit. To stay
// exact, no exported method here returns a value; each writes its own result
// with ret().
package main

import (
	"encoding/json"
	"strconv"

	"github.com/vlmoon99/near-sdk-go/env"
	"github.com/vlmoon99/near-sdk-go/promise"
	"github.com/vlmoon99/near-sdk-go/types"

	"doorlist/contract/core"
)

// @contract:state
type Doorlist struct {
	Version uint32 `json:"version"`
}

// ---- runtime adapters ----

type chainKV struct{}

func (chainKV) Get(k string) ([]byte, bool) {
	ok, _ := env.StorageHasKey([]byte(k))
	if !ok {
		return nil, false
	}
	v, err := env.StorageRead([]byte(k))
	return v, err == nil
}
func (chainKV) Set(k string, v []byte) { env.StorageWrite([]byte(k), v) }
func (chainKV) Del(k string)           { env.StorageRemove([]byte(k)) }

type chainEnv struct{}

func (chainEnv) NowMs() uint64 { return env.GetBlockTimeMs() }
func (chainEnv) Predecessor() string {
	p, _ := env.GetPredecessorAccountID()
	return p
}
func (chainEnv) VerifyEd25519(pub [32]byte, msg []byte, sig [64]byte) bool {
	return env.Ed25519VerifySig(sig, msg, pub)
}

func ledger() *core.Ledger { return core.New(chainKV{}, chainEnv{}) }

func must(err error) {
	if err != nil {
		env.PanicStr(err.Error())
	}
}

func ret(v any) {
	b, err := json.Marshal(v)
	must(err)
	env.ContractValueReturn(b)
}

func yocto(s string) types.Uint128 {
	if s == "" {
		s = "0"
	}
	v, err := types.U128FromString(s)
	must(err)
	return v
}

// run turns core Calls into promises, chaining callbacks onto this contract.
func run(calls []core.Call) {
	self, _ := env.GetCurrentAccountId()
	for _, c := range calls {
		p := env.PromiseBatchCreate([]byte(c.Receiver))
		env.PromiseBatchActionFunctionCall(p, []byte(c.Method), c.Args, yocto(c.Deposit), c.Gas)
		if c.Then != nil {
			env.PromiseThen(p, []byte(self), []byte(c.Then.Method), c.Then.Args, yocto(c.Then.Deposit), c.Then.Gas)
		}
	}
}

// ---- init ----

// @contract:init
func (d *Doorlist) Init(owner string, token string, relayer string) {
	d.Version = 1
	must(ledger().Init(core.Config{Owner: owner, Token: token, Relayer: relayer}))
}

// ---- host ----

// @contract:mutating
func (d *Doorlist) CreateEvent(input core.CreateEventInput) {
	ev, err := ledger().CreateEvent(input)
	must(err)
	env.LogString(`EVENT_JSON:{"standard":"doorlist","version":"1","event":"event_created","data":{"id":"` + ev.ID + `","host":"` + ev.Host + `"}}`)
	ret(ev)
}

// @contract:mutating
func (d *Doorlist) AddDoorKey(event string, key string) {
	must(ledger().AddDoorKey(event, key))
}

// @contract:mutating
func (d *Doorlist) RemoveDoorKey(event string, key string) {
	must(ledger().RemoveDoorKey(event, key))
}

// @contract:mutating
func (d *Doorlist) CancelEvent(event string) {
	ev, err := ledger().Cancel(event)
	must(err)
	ret(ev)
}

// ---- guests ----

// ft_on_transfer from the USDC contract. msg: {"event","ticket","guest"?}.
// Returns the unused amount: "0" on success, the full amount on rejection.
//
// @contract:mutating
func (d *Doorlist) FtOnTransfer(sender_id string, amount string, msg string) {
	amt, err := strconv.ParseUint(amount, 10, 64)
	if err != nil {
		ret(amount)
		return
	}
	refund, calls, err := ledger().OnDeposit(sender_id, amt, msg)
	if err != nil {
		env.LogString("doorlist: deposit rejected: " + err.Error())
	} else {
		env.LogString(`EVENT_JSON:{"standard":"doorlist","version":"1","event":"rsvp","data":{"msg":` + strconv.Quote(msg) + `,"sender":"` + sender_id + `"}}`)
	}
	run(calls)
	ret(strconv.FormatUint(refund, 10))
}

// ---- door ----

// @contract:mutating
func (d *Doorlist) CheckIn(input core.CheckInInput) {
	r, err := ledger().CheckIn(input)
	must(err)
	env.LogString(`EVENT_JSON:{"standard":"doorlist","version":"1","event":"check_in","data":{"event":"` + r.Event + `","ticket":"` + r.Ticket + `"}}`)
	ret(r)
}

// ---- close and payouts ----

// @contract:mutating
func (d *Doorlist) CloseEvent(event string) {
	ev, err := ledger().Close(event)
	must(err)
	ret(ev)
}

// @contract:mutating
func (d *Doorlist) PayoutBatch(event string, limit uint32) {
	calls, err := ledger().PayoutBatch(event, limit)
	must(err)
	run(calls)
	ret(len(calls))
}

type ticketRef struct {
	Event  string `json:"event"`
	Ticket string `json:"ticket"`
}

// @contract:mutating
// @contract:promise_callback
func (d *Doorlist) OnPayoutProposed(input ticketRef, result promise.PromiseResult) {
	var id uint64
	ok := result.Success && json.Unmarshal(result.Data, &id) == nil
	calls, err := ledger().OnPayoutProposed(input.Event, input.Ticket, ok, id)
	must(err)
	run(calls)
}

// @contract:mutating
// @contract:promise_callback
func (d *Doorlist) OnPayoutApproved(input ticketRef, result promise.PromiseResult) {
	must(ledger().OnPayoutApproved(input.Event, input.Ticket, result.Success))
}

// ---- views ----

// @contract:view
func (d *Doorlist) GetConfig() {
	c, err := ledger().Config()
	must(err)
	ret(c)
}

// @contract:view
func (d *Doorlist) GetEvent(event string) {
	ev, err := ledger().Event(event)
	if err != nil {
		ret(nil)
		return
	}
	ret(ev)
}

// @contract:view
func (d *Doorlist) GetRsvp(event string, ticket string) {
	r, err := ledger().RSVP(event, ticket)
	if err != nil {
		ret(nil)
		return
	}
	ret(r)
}

// @contract:view
func (d *Doorlist) ListRsvps(event string, from uint32, limit uint32) {
	rs, err := ledger().RSVPs(event, from, limit)
	must(err)
	ret(rs)
}

// @contract:view
func (d *Doorlist) GetShows(account string) {
	ret(ledger().Shows(account))
}

// @contract:view
func (d *Doorlist) CanRsvp(event string, account string) {
	if err := ledger().CanRSVP(event, account); err != nil {
		ret(err.Error())
		return
	}
	ret("ok")
}

// @contract:view
func (d *Doorlist) GetSplit(deposit string, going uint32, shows uint32) {
	dep, err := strconv.ParseUint(deposit, 10, 64)
	must(err)
	ret(core.Split(dep, going, shows))
}
