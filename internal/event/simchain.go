package event

import (
	"crypto/ed25519"
	"encoding/json"
	"errors"
	"sync"
	"time"

	"doorlist/contract/core"
)

// SimChain runs the real contract core in process, with a simulated USDC
// token and Sputnik DAO around it. Demo mode only: no money moves.
type SimChain struct {
	mu     sync.Mutex
	kv     *core.MemKV
	cfg    core.Config
	now    func() time.Time
	at     *time.Time // fixed time while seeding the past
	caller string
	daos   map[string]*Treasury
	nextID uint64
}

// NewSimChain returns a fresh simulated chain.
func NewSimChain(cfg core.Config, now func() time.Time) *SimChain {
	c := &SimChain{kv: core.NewMemKV(), cfg: cfg, now: now, daos: map[string]*Treasury{}}
	c.caller = cfg.Owner
	if err := c.ledger().Init(cfg); err != nil {
		panic(err)
	}
	return c
}

type simEnv struct{ c *SimChain }

func (e simEnv) NowMs() uint64 {
	if e.c.at != nil {
		return uint64(e.c.at.UnixMilli())
	}
	return uint64(e.c.now().UnixMilli())
}
func (e simEnv) Predecessor() string { return e.c.caller }
func (e simEnv) VerifyEd25519(pub [32]byte, msg []byte, sig [64]byte) bool {
	return ed25519.Verify(pub[:], msg, sig[:])
}

func (c *SimChain) ledger() *core.Ledger { return core.New(c.kv, simEnv{c}) }

// As runs fn with the chain clock fixed at t (used to seed past events).
func (c *SimChain) As(t time.Time, fn func()) {
	c.mu.Lock()
	c.at = &t
	c.mu.Unlock()
	defer func() { c.mu.Lock(); c.at = nil; c.mu.Unlock() }()
	fn()
}

func (c *SimChain) Name() string        { return "simulated (demo)" }
func (c *SimChain) Config() core.Config { return c.cfg }

func (c *SimChain) Event(id string) (*core.Event, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.ledger().Event(id)
}

func (c *SimChain) RSVPs(id string) ([]core.RSVP, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.ledger().RSVPs(id, 0, 5000)
}

func (c *SimChain) RSVP(id, ticket string) (*core.RSVP, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.ledger().RSVP(id, ticket)
}

func (c *SimChain) Shows(guest string) uint32 {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.ledger().Shows(guest)
}

func (c *SimChain) CanRSVP(id, guest string) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.ledger().CanRSVP(id, guest)
}

func (c *SimChain) CreateEvent(host string, in core.CreateEventInput) (*core.Event, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.caller = host
	ev, err := c.ledger().CreateEvent(in)
	if err == nil {
		c.dao(ev.DAO)
	}
	return ev, err
}

func (c *SimChain) AddDoorKey(host, event, key string) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.caller = host
	return c.ledger().AddDoorKey(event, key)
}

func (c *SimChain) Close(caller, event string) (*core.Event, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.caller = caller
	return c.ledger().Close(event)
}

func (c *SimChain) dao(name string) *Treasury {
	t, ok := c.daos[name]
	if !ok {
		t = &Treasury{DAO: name, Simulated: true}
		c.daos[name] = t
	}
	return t
}

// Deposit simulates the relayer's ft_transfer_call into the contract.
func (c *SimChain) Deposit(event, ticket, guest, payTo string, amount uint64) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.caller = c.cfg.Token
	msg, _ := json.Marshal(core.DepositMsg{Event: event, Ticket: ticket, Guest: guest, PayTo: payTo})
	refund, calls, err := c.ledger().OnDeposit(c.cfg.Relayer, amount, string(msg))
	if err != nil {
		return err
	}
	if refund != 0 {
		return errors.New("deposit refunded")
	}
	for _, call := range calls {
		if call.Method == "ft_transfer" {
			var a struct {
				Receiver string `json:"receiver_id"`
				Amount   string `json:"amount"`
			}
			_ = json.Unmarshal(call.Args, &a)
			var v uint64
			_ = json.Unmarshal([]byte(a.Amount), &v)
			c.dao(a.Receiver).Balance += v
		}
	}
	return nil
}

func (c *SimChain) CheckIn(in core.CheckInInput) (*core.RSVP, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.caller = c.cfg.Relayer
	return c.ledger().CheckIn(in)
}

// Payout runs payout batches until nothing is owed, executing the DAO side
// (add_proposal, act_proposal, the USDC transfer) in process.
func (c *SimChain) Payout(event string) (int, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	paid := 0
	for round := 0; round < 200; round++ {
		c.caller = c.cfg.Relayer
		calls, err := c.ledger().PayoutBatch(event, 20)
		if err != nil {
			return paid, err
		}
		if len(calls) == 0 {
			return paid, nil
		}
		for _, call := range calls {
			var cb struct{ Event, Ticket string }
			_ = json.Unmarshal(call.Then.Args, &cb)
			var p struct {
				Proposal struct {
					Description string `json:"description"`
					Kind        struct {
						Transfer struct {
							Receiver string `json:"receiver_id"`
							Amount   string `json:"amount"`
						} `json:"Transfer"`
					} `json:"kind"`
				} `json:"proposal"`
			}
			_ = json.Unmarshal(call.Args, &p)
			var amt uint64
			_ = json.Unmarshal([]byte(p.Proposal.Kind.Transfer.Amount), &amt)
			t := c.dao(call.Receiver)
			c.nextID++
			prop := Proposal{ID: c.nextID, Description: p.Proposal.Description, Receiver: p.Proposal.Kind.Transfer.Receiver, Amount: amt, Status: "InProgress"}

			c.caller = c.cfg.Owner // callbacks come from the contract itself
			votes, err := c.ledger().OnPayoutProposed(cb.Event, cb.Ticket, true, prop.ID)
			if err != nil || len(votes) == 0 {
				return paid, err
			}
			ok := t.Balance >= amt
			if ok {
				t.Balance -= amt
				prop.Status = "Approved"
				paid++
			} else {
				prop.Status = "Failed"
			}
			t.Proposals = append(t.Proposals, prop)
			if err := c.ledger().OnPayoutApproved(cb.Event, cb.Ticket, ok); err != nil {
				return paid, err
			}
			if !ok {
				return paid, errors.New("DAO balance too low for payout")
			}
		}
	}
	return paid, nil
}

func (c *SimChain) Treasury(dao string) Treasury {
	c.mu.Lock()
	defer c.mu.Unlock()
	t := *c.dao(dao)
	t.Proposals = append([]Proposal(nil), t.Proposals...)
	return t
}
