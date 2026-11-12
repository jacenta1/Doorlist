package web

import (
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
	"regexp"
	"strings"
	"testing"
	"time"

	"doorlist/internal/event"
	"doorlist/internal/intents"
)

func newDemo(t *testing.T) (*httptest.Server, *http.Client) {
	t.Helper()
	chain := event.NewSimChain(event.DemoConfig, time.Now)
	svc := event.NewService(chain, intents.WithFallback(intents.NewClient("http://127.0.0.1:1", "")), time.Now, "http://demo.test", true)
	if err := event.Seed(svc, chain); err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer((&Server{Svc: svc, Secret: []byte("test-secret-test-secret"), Demo: true, Network: "testnet"}).Handler())
	t.Cleanup(srv.Close)
	jar, _ := cookiejar.New(nil)
	return srv, &http.Client{Jar: jar}
}

func get(t *testing.T, c *http.Client, u string) (int, string) {
	t.Helper()
	r, err := c.Get(u)
	if err != nil {
		t.Fatal(err)
	}
	defer r.Body.Close()
	b, _ := io.ReadAll(r.Body)
	return r.StatusCode, string(b)
}

func TestPagesRender(t *testing.T) {
	srv, c := newDemo(t)
	want := map[string]string{
		"/":                  "Show up.",
		"/e/builders-night":  "Deposit &amp; RSVP",
		"/e/zk-reading-club": "Settled",
		"/e/founders-dinner": "Regulars first",
		"/t/zk-reading-club/" + event.TicketShowedUp: "You showed up.",
		"/t/zk-reading-club/" + event.TicketFlaked:   "make it.",
		"/break":                    "19 of 19 refused.",
		"/kit":                      "Run it at a real meetup.",
		"/e/builders-night/poster":  "Scan to RSVP",
		"/api/events/rooftop-demos": `"phase":"doors"`,
	}
	for p, s := range want {
		code, body := get(t, c, srv.URL+p)
		if code != 200 || !strings.Contains(body, s) {
			t.Errorf("%s: %d, missing %q", p, code, s)
		}
	}
	if code, _ := get(t, c, srv.URL+"/e/nope"); code != 404 {
		t.Errorf("unknown event: %d", code)
	}
}

// The whole guest and host journey over HTTP: RSVP, simulate deposit, ticket,
// host arrival, close, payout, receipt.
func TestJourney(t *testing.T) {
	srv, c := newDemo(t)
	// Guests can't reach host pages.
	if _, body := get(t, c, srv.URL+"/host/e/rooftop-demos"); !strings.Contains(body, "Continue as demo host") {
		t.Fatal("host page open without sign-in")
	}
	// RSVP (the quoter is unreachable here, so the labelled offline estimate is used).
	r, err := c.PostForm(srv.URL+"/e/rooftop-demos/rsvp", url.Values{"name": {"Late"}, "origin": {"usdc-base"}, "address": {"0x1111111111111111111111111111111111111111"}})
	if err != nil {
		t.Fatal(err)
	}
	b, _ := io.ReadAll(r.Body)
	r.Body.Close()
	if r.StatusCode != 422 || !strings.Contains(string(b), "RSVPs for this event are closed") {
		t.Fatalf("RSVP after start: %d", r.StatusCode)
	}
	r, _ = c.PostForm(srv.URL+"/e/builders-night/rsvp", url.Values{"name": {"Ada"}, "origin": {"usdc-base"}, "address": {"0x1111111111111111111111111111111111111111"}})
	b, _ = io.ReadAll(r.Body)
	r.Body.Close()
	m := regexp.MustCompile(`/t/builders-night/([A-Z2-7]{16})`).FindStringSubmatch(r.Request.URL.Path + string(b))
	if m == nil {
		t.Fatalf("no ticket after RSVP: %d %s", r.StatusCode, r.Request.URL)
	}
	if _, body := get(t, c, srv.URL+m[0]); !strings.Contains(body, "Waiting for your deposit.") {
		t.Fatal("ticket should wait for deposit")
	}
	r, _ = c.PostForm(srv.URL+"/t/"+m[1]+"/simulate", nil)
	r.Body.Close()
	if _, body := get(t, c, srv.URL+m[0]); !strings.Contains(body, "on the list.") || !strings.Contains(body, "<svg") {
		t.Fatal("ticket should show QR after deposit")
	}
	// Host: sign in, bring one more guest in, close the live event.
	r, _ = c.PostForm(srv.URL+"/host/demo", nil)
	r.Body.Close()
	r, _ = c.PostForm(srv.URL+"/host/e/rooftop-demos/arrive", nil)
	r.Body.Close()
	r, _ = c.PostForm(srv.URL+"/host/e/rooftop-demos/close", nil)
	b, _ = io.ReadAll(r.Body)
	r.Body.Close()
	if !strings.Contains(string(b), "12 payouts filed and approved") || !strings.Contains(string(b), "Settled") {
		t.Fatalf("close: %s", r.Request.URL)
	}
	if _, body := get(t, c, srv.URL+"/t/rooftop-demos/"+event.TicketDoor); !strings.Contains(body, "make it.") {
		t.Fatal("no-show ticket after close")
	}
}
