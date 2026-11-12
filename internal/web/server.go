// Package web is the HTTP layer: pages, htmx fragments, the door API and a
// small JSON API.
package web

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io/fs"
	"log"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/a-h/templ"

	"doorlist/contract/core"
	"doorlist/internal/checkin"
	"doorlist/internal/event"
	"doorlist/internal/near"
	"doorlist/internal/pot"
	webassets "doorlist/web"
	"doorlist/web/templates"
)

// Server serves Doorlist.
type Server struct {
	Svc      *event.Service
	Secret   []byte
	Demo     bool
	Network  string
	Contract string
	RPC      *near.RPC // live sign-in checks; nil in demo
	Location *time.Location

	mu     sync.Mutex
	nonces map[string]time.Time
}

// Handler builds the router.
func (s *Server) Handler() http.Handler {
	if s.nonces == nil {
		s.nonces = map[string]time.Time{}
	}
	if s.Location == nil {
		s.Location = time.UTC
	}
	m := http.NewServeMux()
	static, _ := fs.Sub(webassets.Static, "static")
	m.Handle("GET /static/", http.StripPrefix("/static/", cacheStatic(http.FileServer(http.FS(static)))))
	m.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) { w.Write([]byte("ok")) })

	m.HandleFunc("GET /{$}", s.home)
	m.HandleFunc("GET /e/{id}", s.eventPage)
	m.HandleFunc("GET /e/{id}/rsvp", s.rsvpPage)
	m.HandleFunc("POST /e/{id}/rsvp", s.rsvpPost)
	m.HandleFunc("GET /e/{id}/poster", s.poster)
	m.HandleFunc("POST /t/{ticket}/simulate", s.simulate)
	m.HandleFunc("GET /t/{id}/{ticket}", s.ticket)
	m.HandleFunc("GET /t/{id}/{ticket}/status", s.ticketStatus)
	m.HandleFunc("GET /kit", func(w http.ResponseWriter, r *http.Request) { s.render(w, r, templates.Kit(s.site(r))) })

	m.HandleFunc("GET /break", func(w http.ResponseWriter, r *http.Request) {
		s.render(w, r, templates.BreakIt(s.site(r), event.TryToBreakIt()))
	})

	m.HandleFunc("GET /host", s.hostHome)
	m.HandleFunc("POST /host/demo", s.hostDemo)
	m.HandleFunc("POST /host/signin", s.hostSignIn)
	m.HandleFunc("POST /host/signout", s.hostSignOut)
	m.HandleFunc("POST /host/events", s.hostCreate)
	m.HandleFunc("GET /host/e/{id}", s.hostEvent)
	m.HandleFunc("POST /host/e/{id}/close", s.hostClose)
	m.HandleFunc("POST /host/e/{id}/arrive", s.hostArrive)

	m.HandleFunc("GET /door/{id}", s.door)
	m.HandleFunc("POST /door/{id}/register", s.doorRegister)
	m.HandleFunc("POST /door/{id}/scan", s.doorScan)
	m.HandleFunc("GET /door/{id}/lookup", s.doorLookup)

	m.HandleFunc("GET /api/events", s.apiEvents)
	m.HandleFunc("GET /api/events/{id}", s.apiEvent)
	m.HandleFunc("GET /api/tickets/{id}/{ticket}", s.apiTicket)
	m.HandleFunc("POST /api/events/{id}/rsvp", s.apiRSVP)
	m.HandleFunc("GET /api/aura/{guest}", s.apiAura)

	m.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		s.render(w, r, templates.Message(s.site(r), "Wrong door.", "There's nothing at this address.", "/", "See events"))
	})
	return securityHeaders(m)
}

func securityHeaders(h http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Referrer-Policy", "same-origin")
		w.Header().Set("Permissions-Policy", "camera=(self)")
		h.ServeHTTP(w, r)
	})
}

func cacheStatic(h http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "public, max-age=300")
		h.ServeHTTP(w, r)
	})
}

func (s *Server) site(r *http.Request) templates.Site {
	return templates.Site{Demo: s.Demo, Host: s.host(r), ChainName: s.Svc.Chain.Name(), Network: s.Network, Contract: s.Contract, Path: r.URL.Path}
}

func (s *Server) render(w http.ResponseWriter, r *http.Request, c templ.Component) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := c.Render(r.Context(), w); err != nil {
		log.Printf("render %s: %v", r.URL.Path, err)
	}
}

func (s *Server) notFound(w http.ResponseWriter, r *http.Request) {
	w.WriteHeader(http.StatusNotFound)
	s.render(w, r, templates.Message(s.site(r), "Wrong door.", "No event or ticket at this link. Check it with whoever sent it.", "/", "See events"))
}

func (s *Server) view(id string) (*event.View, error) {
	v, err := s.Svc.View(id)
	if err != nil {
		return nil, err
	}
	s.localize(v)
	return v, nil
}

func (s *Server) localize(v *event.View) {
	v.Starts, v.Doors, v.Closes, v.Public = v.Starts.In(s.Location), v.Doors.In(s.Location), v.Closes.In(s.Location), v.Public.In(s.Location)
}

// ---- guest pages ----

func (s *Server) home(w http.ResponseWriter, r *http.Request) {
	evs := s.Svc.Events()
	for _, v := range evs {
		s.localize(v)
	}
	s.render(w, r, templates.Home(s.site(r), evs))
}

func (s *Server) eventPage(w http.ResponseWriter, r *http.Request) {
	v, err := s.view(r.PathValue("id"))
	if err != nil {
		s.notFound(w, r)
		return
	}
	s.render(w, r, templates.EventPage(s.site(r), v))
}

func (s *Server) rsvpPage(w http.ResponseWriter, r *http.Request) {
	v, err := s.view(r.PathValue("id"))
	if err != nil {
		s.notFound(w, r)
		return
	}
	s.render(w, r, templates.RSVPPage(s.site(r), v, event.RSVPInput{}, ""))
}

func friendly(err error) string {
	switch {
	case errors.Is(err, core.ErrPriorityOnly):
		return "This event is open to regulars first. You can RSVP once the public window opens."
	case errors.Is(err, core.ErrDuplicateGuest):
		return "That address already has an RSVP for this event. Open your ticket link instead."
	case errors.Is(err, core.ErrFull):
		return "The event is full."
	case errors.Is(err, core.ErrTooLate), errors.Is(err, core.ErrClosed):
		return "RSVPs for this event are closed."
	}
	var msg string
	if err != nil {
		msg = err.Error()
	}
	if strings.HasPrefix(msg, "1click") {
		return "The deposit quote failed: " + strings.TrimPrefix(msg, "1click: ") + ". Check the address and try again."
	}
	return msg
}

func (s *Server) rsvpPost(w http.ResponseWriter, r *http.Request) {
	v, err := s.view(r.PathValue("id"))
	if err != nil {
		s.notFound(w, r)
		return
	}
	in := event.RSVPInput{Event: v.Ev.ID, Name: r.FormValue("name"), Origin: r.FormValue("origin"), Address: r.FormValue("address")}
	ctx, cancel := context.WithTimeout(r.Context(), 20*time.Second)
	defer cancel()
	g, err := s.Svc.StartRSVP(ctx, in)
	htmx := r.Header.Get("HX-Request") == "true"
	if err != nil {
		w.WriteHeader(http.StatusUnprocessableEntity)
		if htmx {
			// htmx swaps 4xx only when told to.
			w.Header().Set("HX-Reswap", "innerHTML")
			s.render(w, r, templates.RSVPForm(v, in, friendly(err)))
			return
		}
		s.render(w, r, templates.RSVPPage(s.site(r), v, in, friendly(err)))
		return
	}
	url := "/t/" + v.Ev.ID + "/" + g.Ticket
	if htmx {
		s.render(w, r, templates.QuoteCard(s.site(r), v, g, url))
		return
	}
	http.Redirect(w, r, url, http.StatusSeeOther)
}

func (s *Server) simulate(w http.ResponseWriter, r *http.Request) {
	if !s.Demo {
		http.Error(w, "demo only", http.StatusForbidden)
		return
	}
	t := r.PathValue("ticket")
	g, ok := s.Svc.Guest(t)
	if !ok {
		s.notFound(w, r)
		return
	}
	_ = s.Svc.Settle(t)
	url := "/t/" + g.Event + "/" + t
	w.Header().Set("HX-Redirect", url)
	http.Redirect(w, r, url, http.StatusSeeOther)
}

func (s *Server) ticketView(r *http.Request) (*event.TicketView, error) {
	t, err := s.Svc.Ticket(r.PathValue("id"), r.PathValue("ticket"))
	if err != nil {
		return nil, err
	}
	s.localize(t.Event)
	return t, nil
}

func (s *Server) ticket(w http.ResponseWriter, r *http.Request) {
	t, err := s.ticketView(r)
	if err != nil {
		s.notFound(w, r)
		return
	}
	s.render(w, r, templates.TicketPage(s.site(r), t))
}

func (s *Server) ticketStatus(w http.ResponseWriter, r *http.Request) {
	t, err := s.ticketView(r)
	if err != nil {
		w.WriteHeader(http.StatusNotFound)
		return
	}
	s.render(w, r, templates.TicketStatus(t))
}

func (s *Server) poster(w http.ResponseWriter, r *http.Request) {
	v, err := s.view(r.PathValue("id"))
	if err != nil {
		s.notFound(w, r)
		return
	}
	s.render(w, r, templates.Poster(s.site(r), v, s.Svc.BaseURL+"/e/"+v.Ev.ID))
}

// ---- host session ----

const cookieName = "dl_host"

func (s *Server) sign(v string) string {
	m := hmac.New(sha256.New, s.Secret)
	m.Write([]byte(v))
	return base64.RawURLEncoding.EncodeToString(m.Sum(nil))
}

func (s *Server) setHost(w http.ResponseWriter, account string) {
	exp := strconv.FormatInt(time.Now().Add(12*time.Hour).Unix(), 10)
	v := account + "|" + exp
	http.SetCookie(w, &http.Cookie{Name: cookieName, Value: v + "|" + s.sign(v), Path: "/", HttpOnly: true, SameSite: http.SameSiteLaxMode, MaxAge: 12 * 3600})
}

func (s *Server) host(r *http.Request) string {
	c, err := r.Cookie(cookieName)
	if err != nil {
		return ""
	}
	parts := strings.Split(c.Value, "|")
	if len(parts) != 3 || !hmac.Equal([]byte(s.sign(parts[0]+"|"+parts[1])), []byte(parts[2])) {
		return ""
	}
	exp, _ := strconv.ParseInt(parts[1], 10, 64)
	if time.Now().Unix() > exp {
		return ""
	}
	return parts[0]
}

func (s *Server) newNonce() string {
	var b [32]byte
	_, _ = rand.Read(b[:])
	n := base64.StdEncoding.EncodeToString(b[:])
	s.mu.Lock()
	defer s.mu.Unlock()
	for k, t := range s.nonces {
		if time.Since(t) > 10*time.Minute {
			delete(s.nonces, k)
		}
	}
	s.nonces[n] = time.Now()
	return n
}

func (s *Server) useNonce(n string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	t, ok := s.nonces[n]
	delete(s.nonces, n)
	return ok && time.Since(t) < 10*time.Minute
}

const signInMessage = "Sign in to Doorlist as a host"

func (s *Server) hostHome(w http.ResponseWriter, r *http.Request) {
	h := s.host(r)
	if h == "" {
		s.render(w, r, templates.HostSignIn(s.site(r), s.newNonce(), "doorlist", r.URL.Query().Get("err")))
		return
	}
	mine := []*event.View{}
	for _, v := range s.Svc.Events() {
		if v.Ev.Host == h {
			s.localize(v)
			mine = append(mine, v)
		}
	}
	start := s.Svc.Now().In(s.Location).Add(7 * 24 * time.Hour).Truncate(time.Hour)
	s.render(w, r, templates.HostHome(s.site(r), mine, r.URL.Query().Get("err"), start))
}

func (s *Server) hostDemo(w http.ResponseWriter, r *http.Request) {
	if !s.Demo {
		http.Error(w, "demo only", http.StatusForbidden)
		return
	}
	s.setHost(w, event.DemoHost)
	http.Redirect(w, r, "/host", http.StatusSeeOther)
}

// hostSignIn takes a NEP-413 signed message from the wallet (JSON).
func (s *Server) hostSignIn(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Nonce  string             `json:"nonce"`
		Signed near.SignedMessage `json:"signed"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 8<<10)).Decode(&body); err != nil || s.RPC == nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	nb, err := base64.StdEncoding.DecodeString(body.Nonce)
	if err != nil || len(nb) != 32 || !s.useNonce(body.Nonce) {
		http.Error(w, "sign-in expired, reload and try again", http.StatusUnauthorized)
		return
	}
	var nonce [32]byte
	copy(nonce[:], nb)
	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
	defer cancel()
	if err := near.VerifySignIn(ctx, s.RPC, body.Signed, signInMessage, nonce, "doorlist"); err != nil {
		http.Error(w, err.Error(), http.StatusUnauthorized)
		return
	}
	s.setHost(w, body.Signed.AccountID)
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) hostSignOut(w http.ResponseWriter, r *http.Request) {
	http.SetCookie(w, &http.Cookie{Name: cookieName, Value: "", Path: "/", MaxAge: -1})
	http.Redirect(w, r, "/", http.StatusSeeOther)
}

func (s *Server) requireHost(w http.ResponseWriter, r *http.Request) (string, *event.View, bool) {
	h := s.host(r)
	if h == "" {
		http.Redirect(w, r, "/host", http.StatusSeeOther)
		return "", nil, false
	}
	if r.PathValue("id") == "" {
		return h, nil, true
	}
	v, err := s.view(r.PathValue("id"))
	if err != nil {
		s.notFound(w, r)
		return "", nil, false
	}
	if v.Ev.Host != h {
		w.WriteHeader(http.StatusForbidden)
		s.render(w, r, templates.Message(s.site(r), "Not your event.", "Only the host who created this event can run its door and close it.", "/host", "Your events"))
		return "", nil, false
	}
	return h, v, true
}

func atoi(s string) int { n, _ := strconv.Atoi(strings.TrimSpace(s)); return n }

func (s *Server) hostCreate(w http.ResponseWriter, r *http.Request) {
	h, _, ok := s.requireHost(w, r)
	if !ok {
		return
	}
	starts, err := time.ParseInLocation("2006-01-02T15:04", r.FormValue("starts"), s.Location)
	if err != nil {
		http.Redirect(w, r, "/host?err=Pick+a+start+time#new", http.StatusSeeOther)
		return
	}
	v, err := s.Svc.Create(event.CreateInput{
		Host: h, Title: r.FormValue("title"), City: r.FormValue("city"), Venue: r.FormValue("venue"), About: r.FormValue("about"),
		Deposit: r.FormValue("deposit"), Starts: starts, Hours: atoi(r.FormValue("hours")), Capacity: atoi(r.FormValue("capacity")),
		PriorityHours: atoi(r.FormValue("priority_hours")), PriorityMinShows: atoi(r.FormValue("priority_min")), DAO: strings.TrimSpace(r.FormValue("dao")),
	})
	if err != nil {
		msg := err.Error()
		if errors.Is(err, event.ErrNeedsWallet) {
			msg = "In live mode, create the event with your wallet: see scripts/create-event.sh."
		} else if errors.Is(err, core.ErrBadInput) {
			msg = "Check the form: capacity 1 to 2000, a valid DAO account, a start in the future."
		}
		http.Redirect(w, r, "/host?err="+urlq(msg)+"#new", http.StatusSeeOther)
		return
	}
	http.Redirect(w, r, "/host/e/"+v.Ev.ID, http.StatusSeeOther)
}

func urlq(s string) string {
	return strings.NewReplacer(" ", "+", "&", "%26", "#", "%23", "?", "%3F").Replace(s)
}

func (s *Server) hostEvent(w http.ResponseWriter, r *http.Request) {
	_, v, ok := s.requireHost(w, r)
	if !ok {
		return
	}
	s.render(w, r, templates.HostEvent(s.site(r), v, s.Svc.BaseURL+"/door/"+v.Ev.ID, r.URL.Query().Get("msg")))
}

func (s *Server) hostClose(w http.ResponseWriter, r *http.Request) {
	h, v, ok := s.requireHost(w, r)
	if !ok {
		return
	}
	n, err := s.Svc.CloseAndPay(h, v.Ev.ID)
	msg := "Closed. " + strconv.Itoa(n) + " payouts filed and approved in the DAO."
	if err != nil {
		msg = "Couldn't close: " + friendly(err)
		if errors.Is(err, event.ErrNeedsWallet) {
			msg = "Before the check-in window ends, closing needs your wallet signature."
		}
	}
	http.Redirect(w, r, "/host/e/"+v.Ev.ID+"?msg="+urlq(msg), http.StatusSeeOther)
}

// hostArrive simulates one guest reaching the door (demo only): the server's
// demo door key signs the next guest who is not in yet.
func (s *Server) hostArrive(w http.ResponseWriter, r *http.Request) {
	_, v, ok := s.requireHost(w, r)
	if !ok || !s.Demo {
		return
	}
	msg := "Everyone is already in."
	for _, rs := range v.RSVPs {
		if rs.CheckedInAt == 0 {
			res := s.Svc.CheckIn(checkin.Sign(event.DemoDoorKey(), v.Ev.ID, rs.Ticket, s.Svc.Now()))
			msg = res.Name + ": " + res.Message
			break
		}
	}
	http.Redirect(w, r, "/host/e/"+v.Ev.ID+"?msg="+urlq(msg), http.StatusSeeOther)
}

// ---- door ----

func (s *Server) door(w http.ResponseWriter, r *http.Request) {
	_, v, ok := s.requireHost(w, r)
	if !ok {
		return
	}
	s.render(w, r, templates.DoorPage(s.site(r), v, v.Ev.DoorKeys))
}

func (s *Server) doorRegister(w http.ResponseWriter, r *http.Request) {
	h, v, ok := s.requireHost(w, r)
	if !ok {
		return
	}
	key := r.FormValue("key")
	if _, err := core.ParseEd25519Key(key); err != nil {
		http.Error(w, "bad key", http.StatusBadRequest)
		return
	}
	err := s.Svc.Chain.AddDoorKey(h, v.Ev.ID, key)
	switch {
	case err == nil:
		w.WriteHeader(http.StatusNoContent)
	case errors.Is(err, event.ErrNeedsWallet):
		// The page asks the host's wallet to call add_door_key.
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusAccepted)
		_ = json.NewEncoder(w).Encode(map[string]any{"contract": s.Contract, "method": "add_door_key", "args": map[string]string{"event": v.Ev.ID, "key": key}})
	default:
		http.Error(w, err.Error(), http.StatusBadRequest)
	}
}

func (s *Server) doorScan(w http.ResponseWriter, r *http.Request) {
	if _, _, ok := s.requireHost(w, r); !ok {
		return
	}
	var scan checkin.Scan
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4<<10)).Decode(&scan); err != nil || scan.Event != r.PathValue("id") {
		http.Error(w, "bad scan", http.StatusBadRequest)
		return
	}
	s.render(w, r, templates.DoorResult(s.Svc.CheckIn(scan)))
}

func (s *Server) doorLookup(w http.ResponseWriter, r *http.Request) {
	if _, _, ok := s.requireHost(w, r); !ok {
		return
	}
	t, ok := s.Svc.FindByShort(r.PathValue("id"), r.URL.Query().Get("code"))
	if !ok {
		w.WriteHeader(http.StatusNotFound)
		s.render(w, r, templates.DoorResult(event.DoorResult{Message: "No ticket with that code."}))
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]string{"ticket": t})
}

// ---- JSON API (also used by the MCP server) ----

type apiEvent struct {
	ID         string `json:"id"`
	Title      string `json:"title"`
	City       string `json:"city"`
	Phase      string `json:"phase"`
	StartsAt   string `json:"starts_at"`
	Deposit    string `json:"deposit_usdc"`
	Going      int    `json:"going"`
	CheckedIn  int    `json:"checked_in"`
	Pot        string `json:"pot_usdc"`
	DAO        string `json:"dao"`
	DAOBalance string `json:"dao_balance_usdc"`
	PayoutEach string `json:"payout_each_if_closed_now_usdc"`
	URL        string `json:"url"`
}

func (s *Server) toAPI(v *event.View) apiEvent {
	return apiEvent{
		ID: v.Ev.ID, Title: v.Meta.Title, City: v.Meta.City, Phase: string(v.Phase), StartsAt: v.Starts.UTC().Format(time.RFC3339),
		Deposit: pot.USDC(v.Ev.Deposit), Going: v.Going, CheckedIn: v.Shows, Pot: pot.USDC(v.Ev.Pot),
		DAO: v.Ev.DAO, DAOBalance: pot.USDC(v.Treasury.Balance), PayoutEach: pot.USDC(v.Split.PayoutPerShow), URL: s.Svc.BaseURL + "/e/" + v.Ev.ID,
	}
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

func (s *Server) apiEvents(w http.ResponseWriter, r *http.Request) {
	out := []apiEvent{}
	for _, v := range s.Svc.Events() {
		out = append(out, s.toAPI(v))
	}
	writeJSON(w, 200, out)
}

func (s *Server) apiEvent(w http.ResponseWriter, r *http.Request) {
	v, err := s.Svc.View(r.PathValue("id"))
	if err != nil {
		writeJSON(w, 404, map[string]string{"error": "not found"})
		return
	}
	writeJSON(w, 200, s.toAPI(v))
}

func (s *Server) apiTicket(w http.ResponseWriter, r *http.Request) {
	t, err := s.Svc.Ticket(r.PathValue("id"), r.PathValue("ticket"))
	if err != nil {
		writeJSON(w, 404, map[string]string{"error": "not found"})
		return
	}
	out := map[string]any{"event": t.Event.Ev.ID, "status": t.Guest.Status, "on_chain": t.RSVP != nil}
	if t.RSVP != nil {
		out["checked_in"] = t.RSVP.CheckedInAt != 0
		out["pay_status"] = t.RSVP.PayStatus
		out["payout_usdc"] = pot.USDC(t.RSVP.Payout)
	}
	writeJSON(w, 200, out)
}

// apiRSVP starts an RSVP: JSON {"name","origin","address"} in, quote and ticket link out.
func (s *Server) apiRSVP(w http.ResponseWriter, r *http.Request) {
	var in event.RSVPInput
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4<<10)).Decode(&struct {
		Name    *string `json:"name"`
		Origin  *string `json:"origin"`
		Address *string `json:"address"`
	}{&in.Name, &in.Origin, &in.Address}); err != nil {
		writeJSON(w, 400, map[string]string{"error": "bad json"})
		return
	}
	in.Event = r.PathValue("id")
	ctx, cancel := context.WithTimeout(r.Context(), 20*time.Second)
	defer cancel()
	g, err := s.Svc.StartRSVP(ctx, in)
	if err != nil {
		writeJSON(w, 422, map[string]string{"error": friendly(err)})
		return
	}
	out := map[string]any{"ticket": g.Ticket, "ticket_url": s.Svc.BaseURL + "/t/" + g.Event + "/" + g.Ticket, "status": g.Status}
	if g.Quote != nil {
		out["send"] = g.Quote.Quote.AmountInFormatted + " " + g.Origin
		out["deposit_address"] = g.Quote.Quote.DepositAddress
		out["dry_run"] = g.Quote.QuoteRequest.Dry
		out["offline_estimate"] = g.Quote.Offline
	}
	writeJSON(w, 200, out)
}

// apiAura recomputes a guest's attendance from the RSVP history and compares
// it with the counter the ledger keeps.
func (s *Server) apiAura(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, 200, s.Svc.Aura(r.PathValue("guest")))
}
