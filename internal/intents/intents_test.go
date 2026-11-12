package intents

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
	"time"
)

func TestDepositRequest(t *testing.T) {
	o, _ := OriginByID("usdc-base")
	r := DepositRequest(o, 10_000_000, "0x1111111111111111111111111111111111111111", "relay.near", true, time.Unix(0, 0))
	if !r.Dry || r.SwapType != "EXACT_OUTPUT" || r.DestinationAsset != USDCOnNear || r.Amount != "10000000" || r.RecipientType != "DESTINATION_CHAIN" {
		t.Fatalf("%+v", r)
	}
}

func TestValidAddress(t *testing.T) {
	good := map[string]string{"base": "0x1111111111111111111111111111111111111111", "sol": "5ZWj7a1f8tWkjBESHKgrLmXshuXxqeY9SYcfbshpAqPG", "btc": "bc1qar0srrr7xfkvy5l643lydnw9re59gtzzwf5mdq", "tron": "TQn9Y2khEsLJW1ChVWFMSMeRDow5KcbLSE"}
	for chain, a := range good {
		if !ValidAddress(chain, a) {
			t.Errorf("%s rejected %s", chain, a)
		}
	}
	if ValidAddress("base", "0x123") || ValidAddress("sol", "0x1111111111111111111111111111111111111111") {
		t.Fatal("bad address accepted")
	}
}

// The client parses a real recorded 1Click dry quote and sends the request as 1Click expects it.
func TestClientAgainstRecordedQuote(t *testing.T) {
	fixture, err := os.ReadFile("testdata/dry-quote-base-usdc.json")
	if err != nil {
		t.Fatal(err)
	}
	var got QuoteRequest
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v0/quote" || r.Header.Get("Authorization") != "Bearer jwt" {
			w.WriteHeader(400)
			return
		}
		_ = json.NewDecoder(r.Body).Decode(&got)
		w.Write(fixture)
	}))
	defer srv.Close()
	o, _ := OriginByID("usdc-base")
	c := NewClient(srv.URL, "jwt")
	q, err := c.Quote(context.Background(), DepositRequest(o, 10_000_000, "0x1111111111111111111111111111111111111111", "near", true, time.Now()))
	if err != nil {
		t.Fatal(err)
	}
	if q.Quote.AmountInFormatted != "10.107094" || q.Quote.TimeEstimate != 35 || !got.Dry {
		t.Fatalf("%+v %+v", q.Quote, got)
	}
}

func TestFallbackOnlyOnNetworkErrors(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(400)
		w.Write([]byte(`{"message":"refundTo is not valid"}`))
	}))
	o, _ := OriginByID("eth-eth")
	req := DepositRequest(o, 10_000_000, "0x1", "near", true, time.Now())
	if _, err := WithFallback(NewClient(srv.URL, "")).Quote(context.Background(), req); err == nil || err.Error() != "1click: 400 refundTo is not valid" {
		t.Fatalf("API rejection must surface: %v", err)
	}
	srv.Close()
	q, err := WithFallback(NewClient(srv.URL, "")).Quote(context.Background(), req)
	if err != nil || !q.Offline || q.Quote.AmountInFormatted == "" {
		t.Fatalf("offline fallback: %v %+v", err, q)
	}
}
