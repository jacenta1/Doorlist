package checkin

import (
	"crypto/ed25519"
	"testing"
	"time"
)

func TestTicketsAndShortCodes(t *testing.T) {
	a, b := NewTicket(), NewTicket()
	if len(a) != 16 || a == b {
		t.Fatalf("tickets %s %s", a, b)
	}
	if ShortCode("ABCDEFGH") != "ABC-DEF" || NormalizeShort(" abc-def ") != "ABCDEF" {
		t.Fatal("short code")
	}
}

func TestParseScan(t *testing.T) {
	ev, tk, err := ParseScan("https://doorlist.example/t/builders-night/SHOWEDUPDEMOAAAA")
	if err != nil || ev != "builders-night" || tk != "SHOWEDUPDEMOAAAA" {
		t.Fatalf("%s %s %v", ev, tk, err)
	}
	for _, bad := range []string{"", "https://evil.example/", "/t/x/AAAA", "/t/builders-night/lowercase0", "hello"} {
		if _, _, err := ParseScan(bad); err == nil {
			t.Errorf("accepted %q", bad)
		}
	}
}

func TestSignVerify(t *testing.T) {
	_, priv, _ := ed25519.GenerateKey(nil)
	s := Sign(priv, "night", "AAAABBBB", time.UnixMilli(1000))
	if err := s.Verify(); err != nil {
		t.Fatal(err)
	}
	s.Ticket = "AAAACCCC"
	if s.Verify() == nil {
		t.Fatal("tampered scan verified")
	}
}
