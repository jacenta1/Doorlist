package event

import (
	"context"
	"encoding/json"
	"errors"
	"strconv"
	"strings"
	"time"

	"doorlist/contract/core"
	"doorlist/internal/near"
)

// LiveChain reads the deployed contract over RPC and sends relayer
// transactions (deposits, check-ins, payout batches). Host actions are signed
// in the host's wallet, not here.
type LiveChain struct {
	RPC      *near.RPC
	Contract string
	Relayer  *near.Signer // nil: read-only
	cfg      core.Config
}

// NewLiveChain reads the contract config once.
func NewLiveChain(rpc *near.RPC, contract string, relayer *near.Signer) (*LiveChain, error) {
	c := &LiveChain{RPC: rpc, Contract: contract, Relayer: relayer}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	if err := rpc.View(ctx, contract, "get_config", map[string]any{}, &c.cfg); err != nil {
		return nil, err
	}
	return c, nil
}

func ctx() (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.Background(), 30*time.Second)
}

func (c *LiveChain) Name() string        { return "NEAR · " + c.Contract }
func (c *LiveChain) Config() core.Config { return c.cfg }

func (c *LiveChain) view(method string, args any, out any) error {
	x, cancel := ctx()
	defer cancel()
	return c.RPC.View(x, c.Contract, method, args, out)
}

func (c *LiveChain) Event(id string) (*core.Event, error) {
	var ev *core.Event
	if err := c.view("get_event", map[string]any{"event": id}, &ev); err != nil {
		return nil, err
	}
	if ev == nil {
		return nil, core.ErrNotFound
	}
	return ev, nil
}

func (c *LiveChain) RSVPs(id string) ([]core.RSVP, error) {
	all := []core.RSVP{}
	for from := 0; from < 5000; from += 100 {
		var page []core.RSVP
		if err := c.view("list_rsvps", map[string]any{"event": id, "from": from, "limit": 100}, &page); err != nil {
			return nil, err
		}
		all = append(all, page...)
		if len(page) < 100 {
			break
		}
	}
	return all, nil
}

func (c *LiveChain) RSVP(id, ticket string) (*core.RSVP, error) {
	var r *core.RSVP
	if err := c.view("get_rsvp", map[string]any{"event": id, "ticket": ticket}, &r); err != nil {
		return nil, err
	}
	if r == nil {
		return nil, core.ErrNotFound
	}
	return r, nil
}

func (c *LiveChain) Shows(guest string) uint32 {
	var n uint32
	_ = c.view("get_shows", map[string]any{"account": guest}, &n)
	return n
}

func (c *LiveChain) CanRSVP(id, guest string) error {
	var s string
	if err := c.view("can_rsvp", map[string]any{"event": id, "account": guest}, &s); err != nil {
		return err
	}
	if s == "ok" {
		return nil
	}
	for _, e := range []error{core.ErrClosed, core.ErrFull, core.ErrPriorityOnly, core.ErrDuplicateGuest, core.ErrTooLate, core.ErrNotFound} {
		if s == e.Error() {
			return e
		}
	}
	return errors.New(s)
}

func (c *LiveChain) CreateEvent(string, core.CreateEventInput) (*core.Event, error) {
	return nil, ErrNeedsWallet
}
func (c *LiveChain) AddDoorKey(string, string, string) error { return ErrNeedsWallet }

// Close is a relayer call once the window has ended; earlier, the host signs it.
func (c *LiveChain) Close(caller, event string) (*core.Event, error) {
	ev, err := c.Event(event)
	if err != nil {
		return nil, err
	}
	if uint64(time.Now().UnixMilli()) <= ev.CheckinCloseMs || c.Relayer == nil {
		return nil, ErrNeedsWallet
	}
	if _, err := c.call("close_event", map[string]any{"event": event}, "0", 100); err != nil {
		return nil, err
	}
	return c.Event(event)
}

func (c *LiveChain) call(method string, args any, deposit string, tgas uint64) (*near.Outcome, error) {
	if c.Relayer == nil {
		return nil, errors.New("live mode without DOORLIST_RELAYER_KEY is read-only")
	}
	a, _ := json.Marshal(args)
	x, cancel := ctx()
	defer cancel()
	return c.Relayer.Send(x, c.Contract, near.FunctionCall{Method: method, Args: a, Gas: tgas * 1_000_000_000_000, Deposit: near.ParseYocto(deposit)})
}

// Deposit sends the relayer's USDC into the contract with ft_transfer_call.
func (c *LiveChain) Deposit(event, ticket, guest, payTo string, amount uint64) error {
	if c.Relayer == nil {
		return errors.New("no relayer key")
	}
	msg, _ := json.Marshal(core.DepositMsg{Event: event, Ticket: ticket, Guest: guest, PayTo: payTo})
	args, _ := json.Marshal(map[string]any{"receiver_id": c.Contract, "amount": strconv.FormatUint(amount, 10), "msg": string(msg)})
	x, cancel := ctx()
	defer cancel()
	out, err := c.Relayer.Send(x, c.cfg.Token, near.FunctionCall{Method: "ft_transfer_call", Args: args, Gas: 150_000_000_000_000, Deposit: near.ParseYocto("1")})
	if err != nil {
		return err
	}
	// ft_transfer_call returns the amount actually used.
	if strings.Trim(string(out.SuccessJSON), `"`) != strconv.FormatUint(amount, 10) {
		return errors.New("deposit was refunded by the contract")
	}
	return nil
}

func (c *LiveChain) CheckIn(in core.CheckInInput) (*core.RSVP, error) {
	out, err := c.call("check_in", in, "0", 30)
	if err != nil {
		return nil, err
	}
	var r core.RSVP
	_ = json.Unmarshal(out.SuccessJSON, &r)
	return &r, nil
}

func (c *LiveChain) Payout(event string) (int, error) {
	out, err := c.call("payout_batch", map[string]any{"event": event, "limit": 10}, "0", 300)
	if err != nil {
		return 0, err
	}
	var n int
	_ = json.Unmarshal(out.SuccessJSON, &n)
	return n, nil
}

func (c *LiveChain) Treasury(dao string) Treasury {
	t := Treasury{DAO: dao}
	x, cancel := ctx()
	defer cancel()
	var bal string
	if err := c.RPC.View(x, c.cfg.Token, "ft_balance_of", map[string]any{"account_id": dao}, &bal); err == nil {
		t.Balance, _ = strconv.ParseUint(bal, 10, 64)
	}
	var last uint64
	if err := c.RPC.View(x, dao, "get_last_proposal_id", map[string]any{}, &last); err != nil || last == 0 {
		return t
	}
	from := uint64(0)
	if last > 50 {
		from = last - 50
	}
	var props []struct {
		ID          uint64 `json:"id"`
		Description string `json:"description"`
		Status      string `json:"status"`
		Kind        struct {
			Transfer *struct {
				Receiver string `json:"receiver_id"`
				Amount   string `json:"amount"`
			} `json:"Transfer"`
		} `json:"kind"`
	}
	if err := c.RPC.View(x, dao, "get_proposals", map[string]any{"from_index": from, "limit": 50}, &props); err != nil {
		return t
	}
	for _, p := range props {
		if p.Kind.Transfer == nil || !strings.HasPrefix(p.Description, "Doorlist payout") {
			continue
		}
		amt, _ := strconv.ParseUint(p.Kind.Transfer.Amount, 10, 64)
		t.Proposals = append(t.Proposals, Proposal{ID: p.ID, Description: p.Description, Receiver: p.Kind.Transfer.Receiver, Amount: amt, Status: p.Status})
	}
	return t
}
