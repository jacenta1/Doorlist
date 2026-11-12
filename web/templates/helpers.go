// Package templates holds the templ views and their small helpers.
package templates

import (
	"fmt"
	"html"
	"strings"
	"time"

	"rsc.io/qr"

	"doorlist/contract/core"
	"doorlist/internal/event"
	"doorlist/internal/intents"
	"doorlist/internal/pot"
)

// Site is page-wide state.
type Site struct {
	Demo      bool
	Host      string // signed-in host account, if any
	ChainName string
	Network   string
	Contract  string
	Path      string
}

func usdc(v uint64) string  { return pot.USDC2(v) }
func usdcx(v uint64) string { return pot.USDC(v) }
func itoa(v int) string     { return fmt.Sprint(v) }
func u32(v uint32) string   { return fmt.Sprint(v) }

func month(t time.Time) string    { return strings.ToUpper(t.Format("Jan")) }
func day(t time.Time) string      { return t.Format("2") }
func longDate(t time.Time) string { return t.Format("Monday, 2 January") }
func clock(t time.Time) string    { return t.Format("15:04") }
func timeRange(a, b time.Time) string {
	return a.Format("15:04") + " – " + b.Format("15:04") + " " + a.Format("MST")
}
func stamp(t time.Time) string { return t.Format("Mon 2 Jan, 15:04") }

func phaseLabel(p event.Phase) string {
	switch p {
	case event.PhasePriority:
		return "Regulars first"
	case event.PhaseOpen:
		return "RSVPs open"
	case event.PhaseDoors:
		return "Doors open now"
	case event.PhaseEnded:
		return "Check-in over"
	case event.PhaseClosed:
		return "Settled"
	case event.PhaseCancelled:
		return "Cancelled"
	}
	return string(p)
}

func hostLine(hs []event.Host) string {
	names := []string{}
	for _, h := range hs {
		names = append(names, h.Name)
	}
	return strings.Join(names, ", ")
}

func originLabel(id string) string {
	if id == "near" {
		return "NEAR account"
	}
	if o, ok := intents.OriginByID(id); ok {
		return o.Label
	}
	return "—"
}

func payLabel(r core.RSVP) string {
	switch r.PayStatus {
	case core.PayPaid:
		return "Paid " + usdc(r.Payout)
	case core.PayForfeit:
		return "No-show"
	case core.PayOwed, core.PayProposing, core.PayApproving:
		return "Paying " + usdc(r.Payout)
	}
	if r.CheckedInAt != 0 {
		return "In " + clock(time.UnixMilli(int64(r.CheckedInAt)))
	}
	return "Not yet"
}

func payClass(r core.RSVP) string {
	switch {
	case r.PayStatus == core.PayForfeit:
		return "st st-no"
	case r.PayStatus == core.PayPaid || r.CheckedInAt != 0:
		return "st st-in"
	}
	return "st"
}

func rsvpFor(v *event.View, ticket string) core.RSVP {
	for _, r := range v.RSVPs {
		if r.Ticket == ticket {
			return r
		}
	}
	return core.RSVP{}
}

func short(s string) string {
	if len(s) <= 22 {
		return s
	}
	return s[:10] + "…" + s[len(s)-8:]
}

// ---- generated art ----

type palette struct{ bg, ink, accent string }

var palettes = []palette{
	{"#FF5C39", "#141414", "#FFE3D9"},
	{"#1C1C1C", "#FF5C39", "#F4EFE6"},
	{"#F4EFE6", "#141414", "#FF5C39"},
	{"#203A2E", "#F4EFE6", "#FF5C39"},
}

// CoverSVG draws an event's square cover: big stacked words over a simple
// pattern. Same input, same picture.
func CoverSVG(c event.Cover, label string) string {
	p := palettes[((c.Palette%4)+4)%4]
	var b strings.Builder
	fmt.Fprintf(&b, `<svg viewBox="0 0 600 600" xmlns="http://www.w3.org/2000/svg" role="img" aria-label="%s"><rect width="600" height="600" fill="%s"/>`, html.EscapeString(label), p.bg)
	switch c.Pattern % 3 {
	case 0: // rings from the top-right corner
		for i := 6; i >= 1; i-- {
			fill := p.bg
			if i%2 == 1 {
				fill = p.accent
			}
			fmt.Fprintf(&b, `<circle cx="600" cy="0" r="%d" fill="%s" opacity="%.2f"/>`, i*62, fill, 0.35+float64(i%2)*0.3)
		}
	case 1: // a rising sun
		fmt.Fprintf(&b, `<circle cx="430" cy="190" r="130" fill="%s"/>`, p.accent)
		for i := 0; i < 5; i++ {
			fmt.Fprintf(&b, `<rect x="250" y="%d" width="360" height="8" fill="%s"/>`, 150+i*22, p.bg)
		}
	default: // a grid of seats, a few empty
		for y := 0; y < 5; y++ {
			for x := 0; x < 7; x++ {
				f := p.ink
				if (x*3+y*5)%7 == 2 {
					f = p.accent
				}
				fmt.Fprintf(&b, `<circle cx="%d" cy="%d" r="15" fill="%s"/>`, 70+x*76, 60+y*48, f)
			}
		}
	}
	words := c.Words
	if len(words) == 0 {
		words = []string{"DOOR", "LIST"}
	}
	longest := 1
	for _, w := range words {
		if n := len([]rune(w)); n > longest {
			longest = n
		}
	}
	size := 520.0 / (float64(longest) * 0.47)
	if size > 150 {
		size = 150
	}
	lh := size * 0.98
	y := 560.0 - lh*float64(len(words)-1)
	for _, w := range words {
		fmt.Fprintf(&b, `<text x="40" y="%.0f" font-family="Anton, Impact, sans-serif" font-size="%.0f" fill="%s">%s</text>`, y, size, p.ink, html.EscapeString(w))
		y += lh
	}
	b.WriteString(`</svg>`)
	return b.String()
}

// QRSVG renders text as a QR code. Dark modules on white, with a quiet zone,
// so phone cameras read it even on a dark page.
func QRSVG(text string, label string) string {
	code, err := qr.Encode(text, qr.M)
	if err != nil {
		return ""
	}
	n := code.Size
	q := 4
	var b strings.Builder
	fmt.Fprintf(&b, `<svg viewBox="0 0 %d %d" xmlns="http://www.w3.org/2000/svg" role="img" aria-label="%s" shape-rendering="crispEdges"><rect width="100%%" height="100%%" fill="#fff"/><path fill="#141414" d="`, n+2*q, n+2*q, html.EscapeString(label))
	for y := 0; y < n; y++ {
		for x := 0; x < n; x++ {
			if code.Black(x, y) {
				fmt.Fprintf(&b, "M%d %dh1v1h-1z", x+q, y+q)
			}
		}
	}
	b.WriteString(`"/></svg>`)
	return b.String()
}

func pct(a, b int) int {
	if b <= 0 {
		return 0
	}
	return a * 100 / b
}

func ms(v uint64) time.Time { return time.UnixMilli(int64(v)) }
func u64(v uint64) string   { return fmt.Sprint(v) }

func stageClass(t *event.TicketView, i int) string {
	done := 0
	if t.RSVP != nil {
		done = 1
		if t.RSVP.CheckedInAt != 0 {
			done = 2
		}
		if t.RSVP.PayStatus == core.PayPaid {
			done = 3
		}
		if t.RSVP.PayStatus == core.PayForfeit {
			if i == 0 {
				return "done"
			}
			return "lost"
		}
	}
	switch {
	case i < done:
		return "done"
	case i == done:
		return "now"
	}
	return ""
}

func boolStr(b bool) string {
	if b {
		return "1"
	}
	return "0"
}

func joinKeys(k []string) string { return strings.Join(k, ",") }

func defaultDAO(s Site) string {
	if s.Demo {
		return "demo-host-dao.sputnikv2.testnet"
	}
	return ""
}
