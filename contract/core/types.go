// Package core is the Doorlist escrow ledger: events, RSVPs, host-signed
// check-ins, closing and the payout split.
//
// It has no dependency on the NEAR runtime. The near-sdk-go binding in the
// parent directory feeds it a KV store, a clock, the caller and an ed25519
// verifier, and turns the Calls it returns into promises. The web server runs
// the same code in demo mode, so the demo and the contract cannot drift apart.
package core

import "errors"

// KV is contract storage.
type KV interface {
	Get(key string) ([]byte, bool)
	Set(key string, val []byte)
	Del(key string)
}

// Env is the slice of the NEAR runtime the ledger needs.
type Env interface {
	NowMs() uint64
	Predecessor() string
	VerifyEd25519(pub [32]byte, msg []byte, sig [64]byte) bool
}

// Call is an outgoing cross-contract call. If Then is set, it runs on this
// contract after Call finishes and receives Call's result.
type Call struct {
	Receiver string `json:"receiver"`
	Method   string `json:"method"`
	Args     []byte `json:"args"`
	Deposit  string `json:"deposit"` // yoctoNEAR
	Gas      uint64 `json:"gas"`
	Then     *Call  `json:"then,omitempty"`
}

// Config is set once at init.
type Config struct {
	Owner   string `json:"owner"`
	Token   string `json:"token"`   // USDC NEP-141 contract
	Relayer string `json:"relayer"` // deposits on behalf of guests who paid from another chain
}

// Event status values.
const (
	StatusOpen      = "open"
	StatusClosed    = "closed"
	StatusCancelled = "cancelled"
)

// Payout status values for an RSVP.
const (
	PayNone      = ""          // nothing owed yet, or no payout
	PayOwed      = "owed"      // amount computed, no proposal yet
	PayProposing = "proposing" // add_proposal in flight
	PayApproving = "approving" // act_proposal in flight
	PayPaid      = "paid"
	PayForfeit   = "forfeit" // did not show; deposit went to the people who came
)

// Event is one meetup with a deposit.
type Event struct {
	ID               string   `json:"id"`
	Host             string   `json:"host"`
	Title            string   `json:"title"`
	Deposit          uint64   `json:"deposit,string"` // micro-USDC (6 decimals)
	StartsAtMs       uint64   `json:"starts_at_ms"`
	DoorsOpenMs      uint64   `json:"doors_open_ms"`  // check-in allowed from here
	CheckinCloseMs   uint64   `json:"checkin_close_ms"`
	PublicFromMs     uint64   `json:"public_from_ms"` // before this, only guests with enough shows can RSVP
	PriorityMinShows uint32   `json:"priority_min_shows"`
	Capacity         uint32   `json:"capacity"`
	DAO              string   `json:"dao"`      // Sputnik DAO that holds the pot
	DAOBond          string   `json:"dao_bond"` // yoctoNEAR attached to each payout proposal
	DoorKeys         []string `json:"door_keys"`
	Status           string   `json:"status"`
	Tickets          []string `json:"tickets"`
	Going            uint32   `json:"going"`
	Shows            uint32   `json:"shows"`
	Pot              uint64   `json:"pot,string"`
	Forfeit          uint64   `json:"forfeit,string"`
	SharePerShow     uint64   `json:"share_per_show,string"`
	Dust             uint64   `json:"dust,string"` // remainder that stays in the DAO
	PaidOut          uint32   `json:"paid_out"`
}

// RSVP is one guest's deposit for one event.
type RSVP struct {
	Event       string  `json:"event"`
	Ticket      string  `json:"ticket"`
	Guest       string  `json:"guest"`  // identity: a NEAR account, or "<chain>:<address>" for guests who paid from another chain
	PayTo       string  `json:"pay_to"` // NEAR account the DAO pays
	Sender      string  `json:"sender"`
	Deposit     uint64  `json:"deposit,string"`
	At          uint64  `json:"at_ms"`
	CheckedInAt uint64  `json:"checked_in_at_ms"`
	DoorKey     string  `json:"door_key"`
	Payout      uint64  `json:"payout,string"`
	PayStatus   string  `json:"pay_status"`
	ProposalID  *uint64 `json:"proposal_id"`
}

// CreateEventInput is the argument of create_event.
type CreateEventInput struct {
	ID               string `json:"id"`
	Title            string `json:"title"`
	Deposit          string `json:"deposit"`
	StartsAtMs       uint64 `json:"starts_at_ms"`
	DoorsOpenMs      uint64 `json:"doors_open_ms"`
	CheckinCloseMs   uint64 `json:"checkin_close_ms"`
	PublicFromMs     uint64 `json:"public_from_ms"`
	PriorityMinShows uint32 `json:"priority_min_shows"`
	Capacity         uint32 `json:"capacity"`
	DAO              string `json:"dao"`
	DAOBond          string `json:"dao_bond"`
}

// DepositMsg is the msg of ft_transfer_call into this contract.
type DepositMsg struct {
	Event  string `json:"event"`
	Ticket string `json:"ticket"`
	Guest  string `json:"guest"`  // optional when the sender is the guest
	PayTo  string `json:"pay_to"` // optional; defaults to guest
}

// CheckInInput is the argument of check_in.
type CheckInInput struct {
	Event     string `json:"event"`
	Ticket    string `json:"ticket"`
	AtMs      uint64 `json:"at_ms"`
	DoorKey   string `json:"door_key"`  // ed25519:<base58>
	Signature string `json:"signature"` // base58, 64 bytes
}

// Errors. The messages are stable: the web app and tests match on them.
var (
	ErrNotInitialized = errors.New("not initialized")
	ErrUnauthorized   = errors.New("unauthorized")
	ErrBadInput       = errors.New("bad input")
	ErrExists         = errors.New("already exists")
	ErrNotFound       = errors.New("not found")
	ErrClosed         = errors.New("event is not open")
	ErrFull           = errors.New("event is full")
	ErrWrongAmount    = errors.New("deposit amount does not match")
	ErrPriorityOnly   = errors.New("priority RSVPs only: not enough past shows yet")
	ErrDuplicateGuest = errors.New("guest already has an RSVP for this event")
	ErrBadSignature   = errors.New("door signature does not verify")
	ErrUnknownDoorKey = errors.New("door key is not registered for this event")
	ErrOutsideWindow  = errors.New("outside the check-in window")
	ErrAlreadyIn      = errors.New("already checked in")
	ErrTooEarly       = errors.New("too early to close")
	ErrTooLate        = errors.New("too late")
)
