// Package checkin is the door protocol: ticket codes, what the guest's QR
// holds, and the door phone's signed check-in.
//
// A door phone makes its own ed25519 key in the browser. The host registers
// that public key for the event (on chain, with their wallet). Each scan is
// signed by the phone, so the contract accepts a check-in from any relayer as
// long as a registered door signed it: the host never has to approve each
// guest in a wallet popup.
package checkin

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base32"
	"errors"
	"net/url"
	"strings"
	"time"

	"doorlist/contract/core"
)

// NewTicket returns a random 16-character base32 ticket code (80 bits).
func NewTicket() string {
	var b [10]byte
	if _, err := rand.Read(b[:]); err != nil {
		panic(err)
	}
	return base32.StdEncoding.EncodeToString(b[:])
}

// ShortCode is what a host reads aloud or types when a camera fails.
func ShortCode(ticket string) string {
	if len(ticket) < 6 {
		return ticket
	}
	return ticket[:3] + "-" + ticket[3:6]
}

// NormalizeShort turns "abc-def", "ABC DEF" into "ABCDEF".
func NormalizeShort(s string) string {
	s = strings.ToUpper(s)
	return strings.NewReplacer("-", "", " ", "").Replace(s)
}

// TicketURL is what the guest's QR encodes. Opening it on any phone shows the
// ticket; the door scanner reads the event and ticket from the path.
func TicketURL(base, event, ticket string) string {
	return strings.TrimRight(base, "/") + "/t/" + event + "/" + ticket
}

// ErrNotATicket means a scanned code is not a Doorlist ticket.
var ErrNotATicket = errors.New("not a Doorlist ticket")

// ParseScan reads a scanned QR payload (a ticket URL) into event and ticket.
func ParseScan(s string) (event, ticket string, err error) {
	s = strings.TrimSpace(s)
	path := s
	if u, perr := url.Parse(s); perr == nil && u.Path != "" {
		path = u.Path
	}
	parts := strings.Split(strings.Trim(path, "/"), "/")
	if len(parts) < 3 || parts[len(parts)-3] != "t" {
		return "", "", ErrNotATicket
	}
	event, ticket = parts[len(parts)-2], parts[len(parts)-1]
	if !core.ValidEventID(event) || !core.ValidTicket(ticket) {
		return "", "", ErrNotATicket
	}
	return event, ticket, nil
}

// Scan is a signed check-in from a door phone.
type Scan struct {
	Event     string `json:"event"`
	Ticket    string `json:"ticket"`
	AtMs      uint64 `json:"at_ms"`
	DoorKey   string `json:"door_key"`
	Signature string `json:"signature"`
}

// Input converts a scan into the contract's check_in argument.
func (s Scan) Input() core.CheckInInput {
	return core.CheckInInput{Event: s.Event, Ticket: s.Ticket, AtMs: s.AtMs, DoorKey: s.DoorKey, Signature: s.Signature}
}

// Verify checks the door signature off chain, before spending gas on it.
func (s Scan) Verify() error {
	pub, err := core.ParseEd25519Key(s.DoorKey)
	if err != nil {
		return err
	}
	sig, err := core.Base58Decode(s.Signature)
	if err != nil || len(sig) != 64 {
		return core.ErrBadSignature
	}
	if !ed25519.Verify(pub[:], core.CheckinMessage(s.Event, s.Ticket, s.AtMs), sig) {
		return core.ErrBadSignature
	}
	return nil
}

// Sign makes a scan with priv. The browser does the same with WebCrypto; this
// is for tests and the demo door.
func Sign(priv ed25519.PrivateKey, event, ticket string, at time.Time) Scan {
	ms := uint64(at.UnixMilli())
	sig := ed25519.Sign(priv, core.CheckinMessage(event, ticket, ms))
	return Scan{
		Event: event, Ticket: ticket, AtMs: ms,
		DoorKey:   core.FormatEd25519Key(priv.Public().(ed25519.PublicKey)),
		Signature: core.Base58Encode(sig),
	}
}
