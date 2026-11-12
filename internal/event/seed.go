package event

import (
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/base32"
	"fmt"
	"time"

	"doorlist/contract/core"
	"doorlist/internal/checkin"
)

// Demo accounts. All demo names end in .testnet and nothing here exists on
// mainnet; the demo chain is simulated in process.
var DemoConfig = core.Config{Owner: "doorlist-demo.testnet", Token: "usdc.demo.testnet", Relayer: "relay.doorlist-demo.testnet"}

const (
	// DemoHost is who "Continue as demo host" signs in as.
	DemoHost = "demo-host.testnet"
	// DemoRegular is a guest with past shows, for the priority-RSVP demo.
	DemoRegular = "you-demo.testnet"
	// Fixed tickets the README walks through.
	TicketShowedUp = "SHOWEDUPDEMOAAAA"
	TicketFlaked   = "FLAKEDDEMOAAAAAA"
	TicketDoor     = "DOORDEMOAAAAAAAA"
)

// DemoDoorKey is the server-side door used by "simulate an arrival" and the
// seed. Real door phones make their own key in the browser.
func DemoDoorKey() ed25519.PrivateKey {
	seed := sha256.Sum256([]byte("doorlist demo door key"))
	return ed25519.NewKeyFromSeed(seed[:])
}

var demoNames = []string{
	"Ana Ribeiro", "Tomás Kühn", "Ifeoma Obi", "Lucía Paz", "Kenji Mori", "Sara Haddad", "Matteo Rossi",
	"Priya Nair", "Jonas Berg", "Amara Diallo", "Chen Wei", "Noor Aziz", "Diego Salas", "Hana Novak",
	"Ruth Mensah", "Pablo Ortiz", "Elif Demir", "Yusuf Kaya", "Mia Laurent", "Olu Adeyemi", "Ines Duarte",
	"Bea Santos", "Kofi Asante", "Lea Fischer", "Raj Mehta", "Zoe Clarke", "Femi Ojo", "Nina Petrova",
	"Theo Martin", "Aiko Sato", "Marta Gil", "Sam Okafor", "Vera Lind", "Ravi Iyer", "Juno Park",
}

func demoTicket(event string, i int) string {
	h := sha256.Sum256([]byte(fmt.Sprintf("%s/%d", event, i)))
	return base32.StdEncoding.EncodeToString(h[:10])
}

func demoAddress(event string, i int) (origin, address, identity, payTo string) {
	h := sha256.Sum256([]byte(fmt.Sprintf("addr/%s/%d", event, i)))
	switch i % 4 {
	case 0:
		a := fmt.Sprintf("guest%02x.testnet", h[0])
		return "near", a, a, a
	case 1:
		a := fmt.Sprintf("0x%x", h[:20])
		return "usdc-base", a, "base:" + a, ""
	case 2:
		a := fmt.Sprintf("0x%x", h[:20])
		return "usdc-arb", a, "arb:" + a, ""
	default:
		a := core.Base58Encode(h[:32])
		return "usdc-sol", a, "sol:" + a, ""
	}
}

// Seed fills a SimChain with four events, all relative to now so the demo
// always has something upcoming, something live and something settled. It is
// deterministic for a given hour.
func Seed(s *Service, c *SimChain) error {
	now := s.Now()
	base := now.Truncate(time.Hour)
	door := DemoDoorKey()
	doorPub := core.FormatEd25519Key(door.Public().(ed25519.PublicKey))
	h := time.Hour

	type guestSpec struct {
		ticket, name, origin, address, identity, payTo string
		show                                          bool
	}
	run := func(m *Meta, deposit uint64, created, starts time.Time, capacity uint32, public time.Time, minShows uint32, guests []guestSpec, checkinAt time.Time, closeAt *time.Time) error {
		var err error
		c.As(created, func() {
			_, err = c.CreateEvent(DemoHost, core.CreateEventInput{
				ID: m.ID, Title: m.Title, Deposit: fmt.Sprint(deposit),
				StartsAtMs: uint64(starts.UnixMilli()), DoorsOpenMs: uint64(starts.Add(-h).UnixMilli()),
				CheckinCloseMs: uint64(starts.Add(4 * h).UnixMilli()), PublicFromMs: uint64(public.UnixMilli()),
				PriorityMinShows: minShows, Capacity: capacity, DAO: m.Hosts[0].Account + "-dao.sputnikv2.testnet",
			})
			if err == nil {
				err = c.AddDoorKey(DemoHost, m.ID, doorPub)
			}
		})
		if err != nil {
			return fmt.Errorf("seed %s: %w", m.ID, err)
		}
		s.AddMeta(m)
		depositAt := created.Add(30 * time.Minute)
		if public.After(depositAt) && public.Before(now) {
			depositAt = public.Add(30 * time.Minute)
		}
		for i, g := range guests {
			if g.payTo == "" {
				g.payTo = s.Outbox
			}
			s.mu.Lock()
			s.guests[g.ticket] = &Guest{Ticket: g.ticket, Event: m.ID, Name: g.name, Origin: g.origin, Address: g.address, Identity: g.identity, PayTo: g.payTo, Status: GuestConfirmed, CreatedAt: depositAt, Seeded: true}
			s.mu.Unlock()
			at := depositAt.Add(time.Duration(i) * 7 * time.Minute)
			if at.After(now) {
				at = now
			}
			c.As(at, func() { err = c.Deposit(m.ID, g.ticket, g.identity, g.payTo, deposit) })
			if err != nil {
				return fmt.Errorf("seed deposit %s/%d: %w", m.ID, i, err)
			}
		}
		for i, g := range guests {
			if !g.show {
				continue
			}
			at := checkinAt.Add(time.Duration(i) * 90 * time.Second)
			if at.After(now) {
				at = now
			}
			c.As(at, func() { _, err = c.CheckIn(checkin.Sign(door, m.ID, g.ticket, at).Input()) })
			if err != nil {
				return fmt.Errorf("seed check-in %s/%d: %w", m.ID, i, err)
			}
		}
		if closeAt != nil {
			c.As(*closeAt, func() {
				if _, err = c.Close(DemoConfig.Relayer, m.ID); err == nil {
					_, err = c.Payout(m.ID)
				}
			})
		}
		return err
	}
	// People belong to a city's community, so the same guests (and their
	// reputation) show up across that city's events.
	guestsFor := func(community, event string, idx []int, shows func(i int) bool) []guestSpec {
		out := []guestSpec{}
		for k, i := range idx {
			o, a, id, p := demoAddress(community, i)
			out = append(out, guestSpec{demoTicket(event, k), demoNames[(i*7+len(community))%len(demoNames)], o, a, id, p, shows(i)})
		}
		return out
	}
	upTo := func(n int) []int {
		out := make([]int, n)
		for i := range out {
			out[i] = i
		}
		return out
	}

	// 1. Settled last week: 14 going, 11 showed. "You" showed up; one fixed flake.
	zk := &Meta{
		ID: "zk-reading-club", Title: "ZK Reading Club #4", City: "Buenos Aires", Venue: "Café Cifrado", Area: "Palermo",
		About: []string{
			"We read one paper a month, out loud, slowly. This month: a gentle walk through folding schemes.",
			"Bring a pen. The deposit keeps the circle small and the chairs full.",
		},
		Hosts: []Host{{"Lucía Paz", "lucia-paz.testnet"}, {"Diego Salas", "diego-salas.testnet"}},
		Cover: Cover{Words: []string{"ZK", "READING", "CLUB"}, Palette: 2, Pattern: 1},
	}
	zkStart := base.Add(-7 * 24 * h).Add(19 * h).Add(-time.Duration(base.Hour()) * h)
	zkGuests := guestsFor("ba", zk.ID, upTo(12), func(i int) bool { return i%5 != 3 })
	zkGuests = append(zkGuests,
		guestSpec{TicketShowedUp, "You (demo)", "near", DemoRegular, DemoRegular, DemoRegular, true},
		guestSpec{TicketFlaked, "Also you, another time (demo)", "usdc-base", "0x00000000000000000000000000000000000d3e70", "base:0x00000000000000000000000000000000000d3e70", "", false},
	)
	zkClose := zkStart.Add(5 * h)
	if err := run(zk, 5_000_000, zkStart.Add(-10*24*h), zkStart, 16, zkStart.Add(-10*24*h), 0, zkGuests, zkStart.Add(-20*time.Minute), &zkClose); err != nil {
		return err
	}

	// 2. A second past event, so the demo regular has two shows.
	tea := &Meta{
		ID: "protocol-tea", Title: "Protocol Tea", City: "Buenos Aires", Venue: "Casa Té", Area: "Villa Crespo",
		About: []string{"Small table, big questions, very good tea."},
		Hosts: []Host{{"Lucía Paz", "lucia-paz.testnet"}},
		Cover: Cover{Words: []string{"PROTOCOL", "TEA"}, Palette: 3, Pattern: 2},
	}
	teaStart := zkStart.Add(-14 * 24 * h)
	teaGuests := guestsFor("ba", tea.ID, upTo(7), func(i int) bool { return i != 2 })
	teaGuests = append(teaGuests, guestSpec{"TEAREGULARAAAAAA", "You (demo)", "near", DemoRegular, DemoRegular, DemoRegular, true})
	teaClose := teaStart.Add(5 * h)
	if err := run(tea, 5_000_000, teaStart.Add(-5*24*h), teaStart, 10, teaStart.Add(-5*24*h), 0, teaGuests, teaStart.Add(-10*time.Minute), &teaClose); err != nil {
		return err
	}

	// 3. Live now: doors open, 18 going, 11 in. The fixed door ticket is not in yet.
	roof := &Meta{
		ID: "rooftop-demos", Title: "Rooftop Demo Hour", City: "Berlin", Venue: "Dachgarten 9", Area: "Kreuzberg",
		About: []string{
			"Five-minute demos on a roof, one mic, no slides over ten. Builders first, questions after.",
			"Doors are open now. Show your ticket at the stairs.",
		},
		Hosts: []Host{{"Jonas Berg", "jonas-berg.testnet"}, {"Elif Demir", "elif-demir.testnet"}, {"Kenji Mori", "kenji-mori.testnet"}},
		Cover: Cover{Words: []string{"ROOFTOP", "DEMO", "HOUR"}, Palette: 1, Pattern: 0},
	}
	roofStart := base
	if now.Sub(base) < 20*time.Minute {
		roofStart = base.Add(-h)
	}
	roofGuests := guestsFor("ber", roof.ID, upTo(17), func(i int) bool { return i < 11 })
	roofGuests = append(roofGuests, guestSpec{TicketDoor, "Door demo guest", "usdc-arb", "0x00000000000000000000000000000000000d00e5", "arb:0x00000000000000000000000000000000000d00e5", "", false})
	if err := run(roof, 10_000_000, roofStart.Add(-4*24*h), roofStart, 40, roofStart.Add(-4*24*h), 0, roofGuests, roofStart.Add(-40*time.Minute), nil); err != nil {
		return err
	}

	// 4. Upcoming and featured: open to everyone, 23 going.
	bn := &Meta{
		ID: "builders-night", Title: "Builders Night: demos & dumplings", City: "Lisbon", Venue: "Armazém 16", Area: "Marvila",
		About: []string{
			"An evening of short demos from people shipping on NEAR this month, then dumplings on the long table.",
			"RSVP with a 10 USDC deposit. Check in at the door and you get it back, plus an equal share of every deposit from people who didn't come.",
			"Regulars (one check-in or more) got a 24-hour head start on RSVPs.",
		},
		Hosts:    []Host{{"Ines Duarte", "ines-duarte.testnet"}, {"Marta Gil", "marta-gil.testnet"}, {"Pablo Ortiz", "pablo-ortiz.testnet"}},
		Featured: true,
		Cover:    Cover{Words: []string{"BUILDERS", "NIGHT"}, Palette: 0, Pattern: 2},
	}
	bnStart := base.Add(50 * h)
	if err := run(bn, 10_000_000, base.Add(-3*24*h), bnStart, 60, base.Add(-2*24*h), 1, guestsFor("lis", bn.ID, upTo(23), func(int) bool { return false }), bnStart, nil); err != nil {
		return err
	}

	// 5. Priority window open right now: only regulars can RSVP.
	fd := &Meta{
		ID: "founders-dinner", Title: "Founders Dinner", City: "Lisbon", Venue: "Mesa Longa", Area: "Alfama",
		About: []string{
			"Twelve seats, one long table. People who have shown up before get the first 48 hours.",
			"Never checked in to a Doorlist event? You can RSVP once the public window opens.",
		},
		Hosts: []Host{{"Ines Duarte", "ines-duarte.testnet"}},
		Cover: Cover{Words: []string{"FOUNDERS", "DINNER"}, Palette: 3, Pattern: 1},
	}
	fdStart := base.Add(6 * 24 * h)
	// Its first four seats went to Buenos Aires regulars who are travelling:
	// they showed up to both reading club and tea.
	return run(fd, 20_000_000, base.Add(-h), fdStart, 12, base.Add(47*h), 2, guestsFor("ba", fd.ID, []int{0, 1, 4, 5}, func(int) bool { return false }), fdStart, nil)
}
