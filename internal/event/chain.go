package event

import (
	"errors"

	"doorlist/contract/core"
)

// Chain is where the escrow ledger lives: the in-process SimChain in demo
// mode, the deployed contract in live mode.
type Chain interface {
	Name() string
	Config() core.Config

	Event(id string) (*core.Event, error)
	RSVPs(id string) ([]core.RSVP, error)
	RSVP(id, ticket string) (*core.RSVP, error)
	Shows(guest string) uint32
	CanRSVP(id, guest string) error

	// Host actions. In live mode these are signed by the host's wallet in
	// the browser, so LiveChain returns ErrNeedsWallet.
	CreateEvent(host string, in core.CreateEventInput) (*core.Event, error)
	AddDoorKey(host, event, key string) error
	Close(caller, event string) (*core.Event, error)

	// Relayer actions.
	Deposit(event, ticket, guest, payTo string, amount uint64) error
	CheckIn(in core.CheckInInput) (*core.RSVP, error)
	Payout(event string) (int, error)

	// Treasury of the event's DAO, for display.
	Treasury(dao string) Treasury
}

// Treasury is what the DAO holds and the payout proposals it has seen.
type Treasury struct {
	DAO       string
	Balance   uint64
	Proposals []Proposal
	Simulated bool
}

// Proposal is one Transfer proposal in the DAO.
type Proposal struct {
	ID          uint64
	Description string
	Receiver    string
	Amount      uint64
	Status      string // Approved, InProgress, Failed
}

// ErrNeedsWallet means the action must be signed by the host's wallet.
var ErrNeedsWallet = errors.New("this action is signed by the host's wallet")
