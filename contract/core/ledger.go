package core

import (
	"encoding/json"
	"strconv"
)

const (
	tgas = uint64(1_000_000_000_000)

	// MaxDoorKeys is how many door phones one event can register.
	MaxDoorKeys = 8
	// MaxClockSkewMs is how far a door's signed timestamp may sit from block time.
	MaxClockSkewMs = 15 * 60 * 1000
	// OneYocto is the deposit NEP-141 transfers require.
	OneYocto = "1"
)

// Ledger is the contract state machine.
type Ledger struct {
	KV  KV
	Env Env
}

// New returns a ledger over kv and env.
func New(kv KV, env Env) *Ledger { return &Ledger{KV: kv, Env: env} }

// ---- storage helpers ----

func (l *Ledger) get(key string, v any) bool {
	b, ok := l.KV.Get(key)
	if !ok {
		return false
	}
	return json.Unmarshal(b, v) == nil
}

func (l *Ledger) put(key string, v any) {
	b, _ := json.Marshal(v)
	l.KV.Set(key, b)
}

func eventKey(id string) string               { return "e:" + id }
func rsvpKey(ev, ticket string) string        { return "r:" + ev + ":" + ticket }
func guestKey(ev, guest string) string        { return "g:" + ev + ":" + guest }
func repKey(guest string) string              { return "rep:" + guest }

// ---- config ----

// Init stores the config. It can run once.
func (l *Ledger) Init(cfg Config) error {
	if _, ok := l.KV.Get("cfg"); ok {
		return ErrExists
	}
	if !ValidAccount(cfg.Owner) || !ValidAccount(cfg.Token) || !ValidAccount(cfg.Relayer) {
		return ErrBadInput
	}
	l.put("cfg", cfg)
	return nil
}

// Config returns the config.
func (l *Ledger) Config() (Config, error) {
	var c Config
	if !l.get("cfg", &c) {
		return c, ErrNotInitialized
	}
	return c, nil
}

// ---- events ----

// CreateEvent registers an event hosted by the caller.
func (l *Ledger) CreateEvent(in CreateEventInput) (*Event, error) {
	if _, err := l.Config(); err != nil {
		return nil, err
	}
	host := l.Env.Predecessor()
	now := l.Env.NowMs()
	dep, err := strconv.ParseUint(in.Deposit, 10, 64)
	if err != nil || dep == 0 {
		return nil, ErrBadInput
	}
	if !ValidEventID(in.ID) || len(in.Title) == 0 || len(in.Title) > 120 || !ValidAccount(in.DAO) {
		return nil, ErrBadInput
	}
	if in.StartsAtMs <= now || in.CheckinCloseMs <= in.StartsAtMs || in.Capacity == 0 || in.Capacity > 2000 {
		return nil, ErrBadInput
	}
	if in.DoorsOpenMs == 0 {
		in.DoorsOpenMs = in.StartsAtMs - 60*60*1000
	}
	if in.DoorsOpenMs > in.StartsAtMs {
		return nil, ErrBadInput
	}
	if in.PublicFromMs > in.StartsAtMs {
		return nil, ErrBadInput
	}
	bond := in.DAOBond
	if bond == "" {
		bond = "0"
	}
	if _, err := strconv.ParseUint(bond, 10, 64); err != nil && len(bond) > 30 {
		return nil, ErrBadInput
	}
	if _, ok := l.KV.Get(eventKey(in.ID)); ok {
		return nil, ErrExists
	}
	ev := &Event{
		ID: in.ID, Host: host, Title: in.Title, Deposit: dep,
		StartsAtMs: in.StartsAtMs, DoorsOpenMs: in.DoorsOpenMs, CheckinCloseMs: in.CheckinCloseMs,
		PublicFromMs: in.PublicFromMs, PriorityMinShows: in.PriorityMinShows,
		Capacity: in.Capacity, DAO: in.DAO, DAOBond: bond,
		DoorKeys: []string{}, Tickets: []string{}, Status: StatusOpen,
	}
	l.put(eventKey(ev.ID), ev)
	return ev, nil
}

// Event loads an event.
func (l *Ledger) Event(id string) (*Event, error) {
	var ev Event
	if !l.get(eventKey(id), &ev) {
		return nil, ErrNotFound
	}
	return &ev, nil
}

func (l *Ledger) hostEvent(id string) (*Event, error) {
	ev, err := l.Event(id)
	if err != nil {
		return nil, err
	}
	if ev.Host != l.Env.Predecessor() {
		return nil, ErrUnauthorized
	}
	return ev, nil
}

// AddDoorKey lets a door phone's key sign check-ins for this event. Host only.
func (l *Ledger) AddDoorKey(eventID, key string) error {
	ev, err := l.hostEvent(eventID)
	if err != nil {
		return err
	}
	if _, err := ParseEd25519Key(key); err != nil {
		return err
	}
	for _, k := range ev.DoorKeys {
		if k == key {
			return nil
		}
	}
	if len(ev.DoorKeys) >= MaxDoorKeys {
		return ErrBadInput
	}
	ev.DoorKeys = append(ev.DoorKeys, key)
	l.put(eventKey(ev.ID), ev)
	return nil
}

// RemoveDoorKey revokes a door phone. Host only.
func (l *Ledger) RemoveDoorKey(eventID, key string) error {
	ev, err := l.hostEvent(eventID)
	if err != nil {
		return err
	}
	out := ev.DoorKeys[:0]
	for _, k := range ev.DoorKeys {
		if k != key {
			out = append(out, k)
		}
	}
	ev.DoorKeys = out
	l.put(eventKey(ev.ID), ev)
	return nil
}

// ---- RSVP ----

// Shows is a guest's reputation: how many events they checked in to.
func (l *Ledger) Shows(guest string) uint32 {
	var n uint32
	l.get(repKey(guest), &n)
	return n
}

// CanRSVP reports whether guest may RSVP now, and why not.
func (l *Ledger) CanRSVP(eventID, guest string) error {
	ev, err := l.Event(eventID)
	if err != nil {
		return err
	}
	return l.canRSVP(ev, guest)
}

func (l *Ledger) canRSVP(ev *Event, guest string) error {
	now := l.Env.NowMs()
	if ev.Status != StatusOpen {
		return ErrClosed
	}
	if now >= ev.StartsAtMs {
		return ErrTooLate
	}
	if ev.Going >= ev.Capacity {
		return ErrFull
	}
	if now < ev.PublicFromMs && l.Shows(guest) < ev.PriorityMinShows {
		return ErrPriorityOnly
	}
	if _, ok := l.KV.Get(guestKey(ev.ID, guest)); ok {
		return ErrDuplicateGuest
	}
	return nil
}

// OnDeposit handles ft_on_transfer. It returns how much to refund (the whole
// amount when the RSVP is rejected) and the calls to make.
func (l *Ledger) OnDeposit(sender string, amount uint64, msg string) (uint64, []Call, error) {
	cfg, err := l.Config()
	if err != nil {
		return amount, nil, err
	}
	if l.Env.Predecessor() != cfg.Token {
		return amount, nil, ErrUnauthorized
	}
	var m DepositMsg
	if json.Unmarshal([]byte(msg), &m) != nil || !ValidTicket(m.Ticket) {
		return amount, nil, ErrBadInput
	}
	guest, payTo := m.Guest, m.PayTo
	if guest == "" {
		guest = sender
	}
	if payTo == "" {
		payTo = guest
	}
	// Only the relayer may deposit on someone else's behalf.
	if (guest != sender || payTo != sender) && sender != cfg.Relayer {
		return amount, nil, ErrUnauthorized
	}
	if !ValidGuest(guest) || !ValidAccount(payTo) {
		return amount, nil, ErrBadInput
	}
	ev, err := l.Event(m.Event)
	if err != nil {
		return amount, nil, err
	}
	if amount != ev.Deposit {
		return amount, nil, ErrWrongAmount
	}
	if err := l.canRSVP(ev, guest); err != nil {
		return amount, nil, err
	}
	if _, ok := l.KV.Get(rsvpKey(ev.ID, m.Ticket)); ok {
		return amount, nil, ErrExists
	}
	r := RSVP{Event: ev.ID, Ticket: m.Ticket, Guest: guest, PayTo: payTo, Sender: sender, Deposit: amount, At: l.Env.NowMs()}
	l.put(rsvpKey(ev.ID, m.Ticket), r)
	l.KV.Set(guestKey(ev.ID, guest), []byte(`"`+m.Ticket+`"`))
	ev.Tickets = append(ev.Tickets, m.Ticket)
	ev.Going++
	ev.Pot += amount
	l.put(eventKey(ev.ID), ev)

	// The pot lives in the event's DAO, not in this contract.
	args, _ := json.Marshal(map[string]any{"receiver_id": ev.DAO, "amount": u(amount), "memo": "doorlist " + ev.ID + " " + m.Ticket})
	return 0, []Call{{Receiver: cfg.Token, Method: "ft_transfer", Args: args, Deposit: OneYocto, Gas: 15 * tgas}}, nil
}

// RSVP loads one RSVP.
func (l *Ledger) RSVP(eventID, ticket string) (*RSVP, error) {
	var r RSVP
	if !l.get(rsvpKey(eventID, ticket), &r) {
		return nil, ErrNotFound
	}
	return &r, nil
}

// RSVPs lists an event's RSVPs in deposit order.
func (l *Ledger) RSVPs(eventID string, from, limit uint32) ([]RSVP, error) {
	ev, err := l.Event(eventID)
	if err != nil {
		return nil, err
	}
	out := []RSVP{}
	for i := from; i < uint32(len(ev.Tickets)) && uint32(len(out)) < limit; i++ {
		r, err := l.RSVP(eventID, ev.Tickets[i])
		if err == nil {
			out = append(out, *r)
		}
	}
	return out, nil
}

// ---- check-in ----

// CheckinMessage is the exact byte string a door phone signs.
func CheckinMessage(eventID, ticket string, atMs uint64) []byte {
	return []byte("doorlist:checkin:v1|" + eventID + "|" + ticket + "|" + strconv.FormatUint(atMs, 10))
}

// CheckIn records that a guest is at the door. Anyone may submit it (usually
// the relayer); what makes it valid is the signature of a door key the host
// registered.
func (l *Ledger) CheckIn(in CheckInInput) (*RSVP, error) {
	ev, err := l.Event(in.Event)
	if err != nil {
		return nil, err
	}
	if ev.Status != StatusOpen {
		return nil, ErrClosed
	}
	known := false
	for _, k := range ev.DoorKeys {
		if k == in.DoorKey {
			known = true
		}
	}
	if !known {
		return nil, ErrUnknownDoorKey
	}
	pub, err := ParseEd25519Key(in.DoorKey)
	if err != nil {
		return nil, err
	}
	sigB, err := Base58Decode(in.Signature)
	if err != nil || len(sigB) != 64 {
		return nil, ErrBadSignature
	}
	var sig [64]byte
	copy(sig[:], sigB)
	if !l.Env.VerifyEd25519(pub, CheckinMessage(in.Event, in.Ticket, in.AtMs), sig) {
		return nil, ErrBadSignature
	}
	now := l.Env.NowMs()
	if in.AtMs < ev.DoorsOpenMs || in.AtMs > ev.CheckinCloseMs || now < ev.DoorsOpenMs {
		return nil, ErrOutsideWindow
	}
	if now > in.AtMs+MaxClockSkewMs || in.AtMs > now+MaxClockSkewMs {
		return nil, ErrOutsideWindow
	}
	r, err := l.RSVP(in.Event, in.Ticket)
	if err != nil {
		return nil, err
	}
	if r.CheckedInAt != 0 {
		return r, ErrAlreadyIn
	}
	r.CheckedInAt = in.AtMs
	r.DoorKey = in.DoorKey
	l.put(rsvpKey(ev.ID, r.Ticket), r)
	ev.Shows++
	l.put(eventKey(ev.ID), ev)
	l.put(repKey(r.Guest), l.Shows(r.Guest)+1)
	return r, nil
}

// ---- close and payouts ----

// Close ends check-in and fixes everyone's payout. The host can close once
// doors are open; anyone can close after the check-in window ends.
func (l *Ledger) Close(eventID string) (*Event, error) {
	ev, err := l.Event(eventID)
	if err != nil {
		return nil, err
	}
	if ev.Status != StatusOpen {
		return nil, ErrClosed
	}
	now := l.Env.NowMs()
	isHost := l.Env.Predecessor() == ev.Host
	if !(isHost && now >= ev.DoorsOpenMs) && now <= ev.CheckinCloseMs {
		return nil, ErrTooEarly
	}
	s := Split(ev.Deposit, ev.Going, ev.Shows)
	ev.Forfeit, ev.SharePerShow, ev.Dust = s.Forfeit, s.SharePerShow, s.Dust
	ev.Status = StatusClosed
	for _, t := range ev.Tickets {
		r, err := l.RSVP(ev.ID, t)
		if err != nil {
			continue
		}
		if r.CheckedInAt != 0 {
			r.Payout, r.PayStatus = s.PayoutPerShow, PayOwed
		} else {
			r.Payout, r.PayStatus = 0, PayForfeit
		}
		l.put(rsvpKey(ev.ID, t), r)
	}
	l.put(eventKey(ev.ID), ev)
	return ev, nil
}

// Cancel calls the event off before it starts; every deposit is owed back.
// Host only.
func (l *Ledger) Cancel(eventID string) (*Event, error) {
	ev, err := l.hostEvent(eventID)
	if err != nil {
		return nil, err
	}
	if ev.Status != StatusOpen {
		return nil, ErrClosed
	}
	if l.Env.NowMs() >= ev.StartsAtMs {
		return nil, ErrTooLate
	}
	ev.Status = StatusCancelled
	for _, t := range ev.Tickets {
		if r, err := l.RSVP(ev.ID, t); err == nil {
			r.Payout, r.PayStatus = r.Deposit, PayOwed
			l.put(rsvpKey(ev.ID, t), r)
		}
	}
	l.put(eventKey(ev.ID), ev)
	return ev, nil
}

// PayoutBatch asks the DAO to pay up to limit owed guests: one Transfer
// proposal each, which this contract then approves with its "doorlist" role.
// Anyone can call it.
func (l *Ledger) PayoutBatch(eventID string, limit uint32) ([]Call, error) {
	cfg, err := l.Config()
	if err != nil {
		return nil, err
	}
	ev, err := l.Event(eventID)
	if err != nil {
		return nil, err
	}
	if ev.Status == StatusOpen {
		return nil, ErrTooEarly
	}
	if limit == 0 || limit > 20 {
		limit = 20
	}
	calls := []Call{}
	for _, t := range ev.Tickets {
		if uint32(len(calls)) >= limit {
			break
		}
		r, err := l.RSVP(ev.ID, t)
		if err != nil || r.PayStatus != PayOwed {
			continue
		}
		r.PayStatus = PayProposing
		l.put(rsvpKey(ev.ID, t), r)
		args, _ := json.Marshal(map[string]any{"proposal": map[string]any{
			"description": "Doorlist payout · " + ev.ID + " · ticket " + t,
			"kind":        TransferKind(cfg.Token, r.PayTo, r.Payout),
		}})
		cb, _ := json.Marshal(map[string]string{"event": ev.ID, "ticket": t})
		calls = append(calls, Call{
			Receiver: ev.DAO, Method: "add_proposal", Args: args, Deposit: ev.DAOBond, Gas: 20 * tgas,
			Then: &Call{Method: "on_payout_proposed", Args: cb, Deposit: "0", Gas: 60 * tgas},
		})
	}
	return calls, nil
}

// TransferKind is a Sputnik DAO Transfer proposal kind for an NEP-141 token.
func TransferKind(token, receiver string, amount uint64) map[string]any {
	return map[string]any{"Transfer": map[string]any{
		"token_id": token, "receiver_id": receiver, "amount": u(amount), "msg": nil,
	}}
}

// OnPayoutProposed is the callback after add_proposal. On success it votes to
// approve, which makes the DAO send the USDC.
func (l *Ledger) OnPayoutProposed(eventID, ticket string, ok bool, proposalID uint64) ([]Call, error) {
	cfg, err := l.Config()
	if err != nil {
		return nil, err
	}
	ev, err := l.Event(eventID)
	if err != nil {
		return nil, err
	}
	r, err := l.RSVP(eventID, ticket)
	if err != nil {
		return nil, err
	}
	if r.PayStatus != PayProposing {
		return nil, ErrBadInput
	}
	if !ok {
		r.PayStatus = PayOwed // retry with the next batch
		l.put(rsvpKey(eventID, ticket), r)
		return nil, nil
	}
	id := proposalID
	r.ProposalID = &id
	r.PayStatus = PayApproving
	l.put(rsvpKey(eventID, ticket), r)
	args, _ := json.Marshal(map[string]any{
		"id": proposalID, "action": "VoteApprove",
		"proposal": TransferKind(cfg.Token, r.PayTo, r.Payout),
		"memo":     nil,
	})
	cb, _ := json.Marshal(map[string]string{"event": eventID, "ticket": ticket})
	return []Call{{
		Receiver: ev.DAO, Method: "act_proposal", Args: args, Deposit: "0", Gas: 30 * tgas,
		Then: &Call{Method: "on_payout_approved", Args: cb, Deposit: "0", Gas: 10 * tgas},
	}}, nil
}

// OnPayoutApproved is the callback after act_proposal.
func (l *Ledger) OnPayoutApproved(eventID, ticket string, ok bool) error {
	r, err := l.RSVP(eventID, ticket)
	if err != nil {
		return err
	}
	if r.PayStatus != PayApproving {
		return ErrBadInput
	}
	if !ok {
		// The proposal exists; a council member can still approve it in the DAO.
		l.put(rsvpKey(eventID, ticket), r)
		return nil
	}
	r.PayStatus = PayPaid
	l.put(rsvpKey(eventID, ticket), r)
	ev, err := l.Event(eventID)
	if err == nil {
		ev.PaidOut++
		l.put(eventKey(eventID), ev)
	}
	return nil
}

// ---- validation ----

func u(v uint64) string { return strconv.FormatUint(v, 10) }

// ValidAccount is a light NEAR account id check.
func ValidAccount(s string) bool {
	if len(s) < 2 || len(s) > 64 {
		return false
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		if !(c >= 'a' && c <= 'z' || c >= '0' && c <= '9' || c == '.' || c == '-' || c == '_') {
			return false
		}
	}
	return true
}

// ValidGuest accepts a NEAR account or "<chain>:<address>", up to 100
// printable characters without spaces or quotes.
func ValidGuest(s string) bool {
	if ValidAccount(s) {
		return true
	}
	if len(s) < 4 || len(s) > 100 {
		return false
	}
	colon := false
	for i := 0; i < len(s); i++ {
		c := s[i]
		if c <= ' ' || c > '~' || c == '"' || c == '\\' {
			return false
		}
		if c == ':' {
			colon = true
		}
	}
	return colon
}

// ValidEventID allows a-z, 0-9 and '-', 3 to 40 characters.
func ValidEventID(s string) bool {
	if len(s) < 3 || len(s) > 40 {
		return false
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		if !(c >= 'a' && c <= 'z' || c >= '0' && c <= '9' || c == '-') {
			return false
		}
	}
	return true
}

// ValidTicket allows base32 (A-Z, 2-7), 8 to 32 characters.
func ValidTicket(s string) bool {
	if len(s) < 8 || len(s) > 32 {
		return false
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		if !(c >= 'A' && c <= 'Z' || c >= '2' && c <= '7') {
			return false
		}
	}
	return true
}
