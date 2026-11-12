// Package event is the app around the ledger: what an event looks like,
// guests and their deposits, the door, and closing.
package event

import (
	"context"
	"errors"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"doorlist/contract/core"
	"doorlist/internal/checkin"
	"doorlist/internal/intents"
	"doorlist/internal/pot"
)

// Host is a person running the event.
type Host struct {
	Name    string
	Account string
}

// Meta is what the contract does not store: the words and the place.
type Meta struct {
	ID       string
	Title    string
	City     string
	Venue    string
	Area     string
	About    []string
	Hosts    []Host
	Featured bool
	Cover    Cover
}

// Cover picks the generated cover art.
type Cover struct {
	Words   []string // up to three short words, set in the display face
	Palette int      // 0..3
	Pattern int      // 0..2
}

// Guest statuses (off-chain view of an RSVP).
const (
	GuestQuoted    = "quoted"    // has a quote, deposit not seen yet
	GuestConfirmed = "confirmed" // deposit is on chain
	GuestRejected  = "rejected"
)

// Guest is one person who started an RSVP.
type Guest struct {
	Ticket    string
	Event     string
	Name      string
	Origin    string // origin id, or "near"
	Address   string // refund and payout address on the origin chain, or a NEAR account
	Identity  string // the contract's guest id
	PayTo     string
	Status    string
	Quote     *intents.QuoteResponse
	Error     string
	CreatedAt time.Time
	Seeded    bool
}

// Service holds everything the web layer needs.
type Service struct {
	mu      sync.RWMutex
	Chain   Chain
	Quoter  intents.Quoter
	Now     func() time.Time
	BaseURL string
	Demo    bool
	// Outbox is the NEAR account that receives payouts for guests who paid
	// from another chain; the relayer forwards them back through 1Click.
	Outbox string
	// LiveQuotes: request real (dry:false) quotes with deposit addresses.
	// Off in demo mode, where quotes are dry:true.
	LiveQuotes bool

	metas  map[string]*Meta
	order  []string
	guests map[string]*Guest
}

// NewService wires a service.
func NewService(chain Chain, q intents.Quoter, now func() time.Time, baseURL string, demo bool) *Service {
	return &Service{
		Chain: chain, Quoter: q, Now: now, BaseURL: baseURL, Demo: demo,
		Outbox: "outbox." + chain.Config().Relayer,
		metas:  map[string]*Meta{}, guests: map[string]*Guest{},
	}
}

// Errors shown to people.
var (
	ErrName    = errors.New("Add the name the host will see on the list.")
	ErrOrigin  = errors.New("Pick how you'll pay.")
	ErrAddress = errors.New("That doesn't look like an address on that chain.")
	ErrNoEvent = errors.New("No event with that link.")
)

// AddMeta registers an event's words. Used by seed and create.
func (s *Service) AddMeta(m *Meta) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.metas[m.ID]; !ok {
		s.order = append(s.order, m.ID)
	}
	s.metas[m.ID] = m
}

// Phase is where an event is in its life, from a guest's point of view.
type Phase string

const (
	PhasePriority  Phase = "priority" // only regulars can RSVP
	PhaseOpen      Phase = "open"
	PhaseDoors     Phase = "doors" // check-in open
	PhaseEnded     Phase = "ended" // window over, not closed yet
	PhaseClosed    Phase = "closed"
	PhaseCancelled Phase = "cancelled"
)

// View is an event as pages show it.
type View struct {
	Meta     *Meta
	Ev       *core.Event
	Phase    Phase
	Starts   time.Time
	Doors    time.Time
	Closes   time.Time
	Public   time.Time
	Going    int
	Shows    int
	SpotsLeft int
	Guests   []*Guest // confirmed, in RSVP order
	RSVPs    []core.RSVP
	Split    core.SplitResult
	Outlooks []pot.Outlook
	Treasury Treasury
}

func ms(v uint64) time.Time { return time.UnixMilli(int64(v)) }

// Events lists events, featured and upcoming first.
func (s *Service) Events() []*View {
	s.mu.RLock()
	ids := append([]string(nil), s.order...)
	s.mu.RUnlock()
	out := []*View{}
	for _, id := range ids {
		if v, err := s.View(id); err == nil {
			out = append(out, v)
		}
	}
	rank := func(v *View) int {
		switch v.Phase {
		case PhaseDoors:
			return 0
		case PhaseOpen, PhasePriority:
			return 1
		case PhaseEnded:
			return 2
		}
		return 3
	}
	sort.SliceStable(out, func(i, j int) bool {
		if rank(out[i]) != rank(out[j]) {
			return rank(out[i]) < rank(out[j])
		}
		return out[i].Starts.Before(out[j].Starts)
	})
	return out
}

// View loads one event.
func (s *Service) View(id string) (*View, error) {
	s.mu.RLock()
	m := s.metas[id]
	s.mu.RUnlock()
	ev, err := s.Chain.Event(id)
	if err != nil {
		return nil, ErrNoEvent
	}
	if m == nil {
		m = &Meta{ID: id, Title: ev.Title, Hosts: []Host{{Name: ev.Host, Account: ev.Host}}, Cover: Cover{Words: firstWords(ev.Title)}}
	}
	v := &View{Meta: m, Ev: ev, Starts: ms(ev.StartsAtMs), Doors: ms(ev.DoorsOpenMs), Closes: ms(ev.CheckinCloseMs), Public: ms(ev.PublicFromMs)}
	v.Phase = s.phase(ev)
	v.RSVPs, _ = s.Chain.RSVPs(id)
	v.Going, v.Shows = int(ev.Going), int(ev.Shows)
	v.SpotsLeft = int(ev.Capacity) - int(ev.Going)
	v.Split = core.Split(ev.Deposit, ev.Going, ev.Shows)
	v.Outlooks = pot.Projections(ev.Deposit, ev.Going, 10, 20, 30)
	v.Treasury = s.Chain.Treasury(ev.DAO)
	s.mu.RLock()
	for _, r := range v.RSVPs {
		if g, ok := s.guests[r.Ticket]; ok {
			v.Guests = append(v.Guests, g)
		} else {
			v.Guests = append(v.Guests, &Guest{Ticket: r.Ticket, Event: id, Name: shortName(r.Guest), Identity: r.Guest, Status: GuestConfirmed})
		}
	}
	s.mu.RUnlock()
	return v, nil
}

func (s *Service) phase(ev *core.Event) Phase {
	now := uint64(s.Now().UnixMilli())
	switch {
	case ev.Status == core.StatusCancelled:
		return PhaseCancelled
	case ev.Status == core.StatusClosed:
		return PhaseClosed
	case now > ev.CheckinCloseMs:
		return PhaseEnded
	case now >= ev.DoorsOpenMs:
		return PhaseDoors
	case now < ev.PublicFromMs:
		return PhasePriority
	}
	return PhaseOpen
}

// RSVPInput is the guest form.
type RSVPInput struct {
	Event   string
	Name    string
	Origin  string
	Address string
}

// StartRSVP validates the form, checks the guest may RSVP and gets a quote
// for the deposit.
func (s *Service) StartRSVP(ctx context.Context, in RSVPInput) (*Guest, error) {
	ev, err := s.Chain.Event(in.Event)
	if err != nil {
		return nil, ErrNoEvent
	}
	in.Name = strings.TrimSpace(in.Name)
	in.Address = strings.TrimSpace(in.Address)
	if in.Name == "" || utf8.RuneCountInString(in.Name) > 40 {
		return nil, ErrName
	}
	g := &Guest{Ticket: checkin.NewTicket(), Event: ev.ID, Name: in.Name, Origin: in.Origin, Address: in.Address, CreatedAt: s.Now()}
	if in.Origin == "near" {
		a := strings.ToLower(in.Address)
		if !core.ValidAccount(a) {
			return nil, ErrAddress
		}
		g.Address, g.Identity, g.PayTo = a, a, a
	} else {
		o, ok := intents.OriginByID(in.Origin)
		if !ok {
			return nil, ErrOrigin
		}
		if !intents.ValidAddress(o.Chain, in.Address) {
			return nil, ErrAddress
		}
		id := o.Chain + ":" + in.Address
		if o.Chain != "sol" && o.Chain != "btc" && o.Chain != "tron" {
			id = strings.ToLower(id)
		}
		g.Identity, g.PayTo = id, s.Outbox
	}
	if err := s.Chain.CanRSVP(ev.ID, g.Identity); err != nil {
		return nil, err
	}
	if in.Origin != "near" {
		o, _ := intents.OriginByID(in.Origin)
		req := intents.DepositRequest(o, ev.Deposit, g.Address, s.Chain.Config().Relayer, !s.LiveQuotes, s.Now())
		q, err := s.Quoter.Quote(ctx, req)
		if err != nil {
			return nil, err
		}
		g.Quote = q
	}
	g.Status = GuestQuoted
	s.mu.Lock()
	s.guests[g.Ticket] = g
	s.mu.Unlock()
	return g, nil
}

// Guest looks up a guest by ticket.
func (s *Service) Guest(ticket string) (*Guest, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	g, ok := s.guests[ticket]
	return g, ok
}

// Settle is called when the guest's deposit has arrived as USDC on NEAR (in
// live mode, when 1Click reports SUCCESS; in demo mode, from the "simulate
// deposit" button). It puts the RSVP on chain.
func (s *Service) Settle(ticket string) error {
	g, ok := s.Guest(ticket)
	if !ok {
		return core.ErrNotFound
	}
	if g.Status == GuestConfirmed {
		return nil
	}
	ev, err := s.Chain.Event(g.Event)
	if err != nil {
		return err
	}
	err = s.Chain.Deposit(g.Event, g.Ticket, g.Identity, g.PayTo, ev.Deposit)
	s.mu.Lock()
	defer s.mu.Unlock()
	if err != nil {
		g.Status, g.Error = GuestRejected, err.Error()
		return err
	}
	g.Status = GuestConfirmed
	return nil
}

// TicketView is the guest's ticket page.
type TicketView struct {
	Event  *View
	Guest  *Guest
	RSVP   *core.RSVP // nil until the deposit is on chain
	URL    string
	Short  string
	Shows  uint32
}

// Ticket loads a ticket.
func (s *Service) Ticket(event, ticket string) (*TicketView, error) {
	v, err := s.View(event)
	if err != nil {
		return nil, err
	}
	g, ok := s.Guest(ticket)
	r, rerr := s.Chain.RSVP(event, ticket)
	if !ok && rerr != nil {
		return nil, core.ErrNotFound
	}
	if !ok {
		g = &Guest{Ticket: ticket, Event: event, Name: shortName(r.Guest), Identity: r.Guest, Status: GuestConfirmed}
	}
	if g.Event != event {
		return nil, core.ErrNotFound
	}
	t := &TicketView{Event: v, Guest: g, URL: checkin.TicketURL(s.BaseURL, event, ticket), Short: checkin.ShortCode(ticket)}
	if rerr == nil {
		t.RSVP = r
	}
	t.Shows = s.Chain.Shows(g.Identity)
	return t, nil
}

// DoorResult is what the door phone shows after a scan.
type DoorResult struct {
	OK      bool
	Name    string
	Ticket  string
	Message string
	Shows   int
	Going   int
}

// FindByShort resolves a typed short code to a ticket of the event.
func (s *Service) FindByShort(event, short string) (string, bool) {
	short = checkin.NormalizeShort(short)
	if len(short) < 6 {
		return "", false
	}
	rs, _ := s.Chain.RSVPs(event)
	for _, r := range rs {
		if strings.HasPrefix(r.Ticket, short) {
			return r.Ticket, true
		}
	}
	return "", false
}

// CheckIn takes a signed scan from a door phone.
func (s *Service) CheckIn(scan checkin.Scan) DoorResult {
	res := DoorResult{Ticket: scan.Ticket}
	if err := scan.Verify(); err != nil {
		res.Message = "This door phone's signature didn't check out. Reload the door page."
		return res
	}
	name := scan.Ticket
	if g, ok := s.Guest(scan.Ticket); ok {
		name = g.Name
	} else if r, err := s.Chain.RSVP(scan.Event, scan.Ticket); err == nil {
		name = shortName(r.Guest)
	}
	res.Name = name
	_, err := s.Chain.CheckIn(scan.Input())
	if ev, e := s.Chain.Event(scan.Event); e == nil {
		res.Shows, res.Going = int(ev.Shows), int(ev.Going)
	}
	switch {
	case err == nil:
		res.OK, res.Message = true, "Checked in. Deposit marked for refund."
	case errors.Is(err, core.ErrAlreadyIn):
		res.Message = "Already checked in."
	case errors.Is(err, core.ErrNotFound):
		res.Name, res.Message = "", "Not on the list for this event."
	case errors.Is(err, core.ErrUnknownDoorKey):
		res.Message = "This phone isn't registered as a door for this event."
	case errors.Is(err, core.ErrOutsideWindow):
		res.Message = "Check-in isn't open right now."
	case errors.Is(err, core.ErrClosed):
		res.Message = "This event is closed."
	default:
		res.Message = err.Error()
	}
	return res
}

// CloseAndPay closes the event and runs payouts.
func (s *Service) CloseAndPay(caller, event string) (int, error) {
	if _, err := s.Chain.Close(caller, event); err != nil && !errors.Is(err, core.ErrClosed) {
		return 0, err
	}
	return s.Chain.Payout(event)
}

func shortName(id string) string {
	if i := strings.IndexByte(id, ':'); i > 0 && len(id) > i+8 {
		a := id[i+1:]
		return a[:6] + "…" + a[len(a)-4:]
	}
	return id
}

func firstWords(title string) []string {
	f := strings.Fields(strings.ToUpper(title))
	if len(f) > 3 {
		f = f[:3]
	}
	return f
}

// Initials for avatar chips.
func Initials(name string) string {
	f := strings.Fields(name)
	out := ""
	for _, w := range f {
		r, _ := utf8.DecodeRuneInString(w)
		out += strings.ToUpper(string(r))
		if len(out) >= 2 {
			break
		}
	}
	if out == "" {
		return "?"
	}
	return out
}

// CreateInput is the host's new-event form.
type CreateInput struct {
	Host             string
	Title            string
	City             string
	Venue            string
	Area             string
	About            string
	Deposit          string // USDC, e.g. "10"
	Starts           time.Time
	Hours            int // check-in stays open this long after the start
	Capacity         int
	PriorityHours    int // regulars get this head start
	PriorityMinShows int
	DAO              string
}

// Create registers an event on the chain and keeps its words.
func (s *Service) Create(in CreateInput) (*View, error) {
	dep, ok := pot.ParseUSDC(in.Deposit)
	if !ok || dep == 0 || dep > 1000_000_000 {
		return nil, errors.New("Deposit must be between 0.01 and 1000 USDC.")
	}
	in.Title = strings.TrimSpace(in.Title)
	if in.Title == "" {
		return nil, errors.New("Give the event a title.")
	}
	if !in.Starts.After(s.Now()) {
		return nil, errors.New("The start time must be in the future.")
	}
	if in.Hours <= 0 {
		in.Hours = 3
	}
	starts := uint64(in.Starts.UnixMilli())
	id := Slug(in.Title) + "-" + strings.ToLower(checkin.NewTicket()[:4])
	public := uint64(0)
	if in.PriorityHours > 0 {
		public = starts - uint64(in.PriorityHours)*3600_000
		if public < uint64(s.Now().UnixMilli()) {
			public = uint64(s.Now().UnixMilli())
		}
	}
	ev, err := s.Chain.CreateEvent(in.Host, core.CreateEventInput{
		ID: id, Title: in.Title, Deposit: strconvU(dep),
		StartsAtMs: starts, DoorsOpenMs: starts - 3600_000, CheckinCloseMs: starts + uint64(in.Hours)*3600_000,
		PublicFromMs: public, PriorityMinShows: uint32(in.PriorityMinShows),
		Capacity: uint32(in.Capacity), DAO: in.DAO,
	})
	if err != nil {
		return nil, err
	}
	about := []string{}
	for _, p := range strings.Split(in.About, "\n\n") {
		if p = strings.TrimSpace(p); p != "" {
			about = append(about, p)
		}
	}
	s.AddMeta(&Meta{
		ID: ev.ID, Title: in.Title, City: strings.TrimSpace(in.City), Venue: strings.TrimSpace(in.Venue), Area: strings.TrimSpace(in.Area),
		About: about, Hosts: []Host{{Name: in.Host, Account: in.Host}},
		Cover: Cover{Words: firstWords(in.Title), Palette: int(starts/3600_000) % 4, Pattern: len(in.Title) % 3},
	})
	return s.View(ev.ID)
}

// Slug makes an id-safe slug, at most 24 characters.
func Slug(s string) string {
	var b strings.Builder
	dash := false
	for _, r := range strings.ToLower(s) {
		switch {
		case r >= 'a' && r <= 'z' || r >= '0' && r <= '9':
			b.WriteRune(r)
			dash = false
		case !dash && b.Len() > 0:
			b.WriteByte('-')
			dash = true
		}
		if b.Len() >= 24 {
			break
		}
	}
	out := strings.Trim(b.String(), "-")
	if len(out) < 3 {
		out = "event"
	}
	return out
}

func strconvU(v uint64) string { return pot.Itoa(v) }

// PendingDeposits lists guests with a live deposit address not yet settled.
func (s *Service) PendingDeposits() []*Guest {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := []*Guest{}
	for _, g := range s.guests {
		if g.Status == GuestQuoted && g.Quote != nil && g.Quote.Quote.DepositAddress != "" {
			out = append(out, g)
		}
	}
	return out
}
