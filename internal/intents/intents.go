// Package intents talks to the NEAR Intents 1Click API: a guest pays the
// deposit from any chain and token, and it settles as USDC on NEAR.
package intents

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// DefaultBaseURL is the public 1Click API.
const DefaultBaseURL = "https://1click.chaindefuser.com"

// USDCOnNear is native USDC on NEAR mainnet.
const USDCOnNear = "nep141:17208628f84f5d6ad33f0da3bbbeb27ffcb398eac501a31bd6ad2011e36133a1"

// Origin is a chain and token a guest can pay with.
type Origin struct {
	ID        string  `json:"id"`
	Label     string  `json:"label"`
	Chain     string  `json:"chain"` // 1Click blockchain id
	ChainName string  `json:"chain_name"`
	Symbol    string  `json:"symbol"`
	AssetID   string  `json:"asset_id"`
	Decimals  int     `json:"decimals"`
	DemoPrice float64 `json:"-"` // USD; only for offline demo quotes (snapshot 2026-11-10)
}

// Origins are the payment options shown to guests. Asset ids come from
// GET /v0/tokens.
var Origins = []Origin{
	{"usdc-base", "USDC on Base", "base", "Base", "USDC", "nep141:base-0x833589fcd6edb6e08f4c7c32d4f71b54bda02913.omft.near", 6, 1},
	{"usdc-arb", "USDC on Arbitrum", "arb", "Arbitrum", "USDC", "nep141:arb-0xaf88d065e77c8cc2239327c5edb3a432268e5831.omft.near", 6, 1},
	{"usdc-sol", "USDC on Solana", "sol", "Solana", "USDC", "nep141:sol-5ce3bf3a31af18be40ba30f721101b4341690186.omft.near", 6, 1},
	{"usdc-eth", "USDC on Ethereum", "eth", "Ethereum", "USDC", "nep141:eth-0xa0b86991c6218b36c1d19d4a2e9eb0ce3606eb48.omft.near", 6, 1},
	{"usdt-tron", "USDT on Tron", "tron", "Tron", "USDT", "nep141:tron-d28a265909efecdcee7c5028585214ea0b96f015.omft.near", 6, 1},
	{"eth-base", "ETH on Base", "base", "Base", "ETH", "nep141:base.omft.near", 18, 2115.06},
	{"eth-eth", "ETH on Ethereum", "eth", "Ethereum", "ETH", "nep141:eth.omft.near", 18, 2115.06},
	{"sol-sol", "SOL on Solana", "sol", "Solana", "SOL", "nep141:sol.omft.near", 9, 86},
	{"btc-btc", "BTC on Bitcoin", "btc", "Bitcoin", "BTC", "nep141:btc.omft.near", 8, 74545},
}

// OriginByID finds an origin.
func OriginByID(id string) (Origin, bool) {
	for _, o := range Origins {
		if o.ID == id {
			return o, true
		}
	}
	return Origin{}, false
}

var (
	evmAddr  = regexp.MustCompile(`^0x[0-9a-fA-F]{40}$`)
	solAddr  = regexp.MustCompile(`^[1-9A-HJ-NP-Za-km-z]{32,44}$`)
	btcAddr  = regexp.MustCompile(`^(bc1[02-9ac-hj-np-z]{11,71}|[13][1-9A-HJ-NP-Za-km-z]{25,34})$`)
	tronAddr = regexp.MustCompile(`^T[1-9A-HJ-NP-Za-km-z]{33}$`)
)

// ValidAddress checks that addr looks like an address on chain. It is a
// format check; 1Click does the final validation.
func ValidAddress(chain, addr string) bool {
	switch chain {
	case "eth", "base", "arb":
		return evmAddr.MatchString(addr)
	case "sol":
		return solAddr.MatchString(addr)
	case "btc":
		return btcAddr.MatchString(addr)
	case "tron":
		return tronAddr.MatchString(addr)
	}
	return false
}

// QuoteRequest is the body of POST /v0/quote.
type QuoteRequest struct {
	Dry               bool   `json:"dry"`
	SwapType          string `json:"swapType"`
	SlippageTolerance int    `json:"slippageTolerance"`
	OriginAsset       string `json:"originAsset"`
	DepositType       string `json:"depositType"`
	DestinationAsset  string `json:"destinationAsset"`
	Amount            string `json:"amount"`
	RefundTo          string `json:"refundTo"`
	RefundType        string `json:"refundType"`
	Recipient         string `json:"recipient"`
	RecipientType     string `json:"recipientType"`
	Deadline          string `json:"deadline"`
}

// Quote is the quote part of the response.
type Quote struct {
	DepositAddress    string `json:"depositAddress,omitempty"`
	AmountIn          string `json:"amountIn"`
	AmountInFormatted string `json:"amountInFormatted"`
	AmountInUSD       string `json:"amountInUsd"`
	AmountOut         string `json:"amountOut"`
	AmountOutFormatted string `json:"amountOutFormatted"`
	TimeEstimate      int    `json:"timeEstimate"`
	Deadline          string `json:"deadline,omitempty"`
}

// QuoteResponse is the full response.
type QuoteResponse struct {
	Quote         Quote        `json:"quote"`
	QuoteRequest  QuoteRequest `json:"quoteRequest"`
	Signature     string       `json:"signature"`
	Timestamp     string       `json:"timestamp"`
	CorrelationID string       `json:"correlationId"`
	// Offline is set when the quote was computed locally because the API
	// could not be reached. It is labelled in the UI.
	Offline bool `json:"offline,omitempty"`
}

// Quoter gets quotes and deposit status.
type Quoter interface {
	Quote(ctx context.Context, req QuoteRequest) (*QuoteResponse, error)
	Status(ctx context.Context, depositAddress string) (string, error)
}

// Client is the HTTP client for 1Click.
type Client struct {
	BaseURL string
	JWT     string // optional; without it 1Click adds its unauthenticated fee
	HTTP    *http.Client
}

// NewClient returns a client with sane timeouts.
func NewClient(baseURL, jwt string) *Client {
	if baseURL == "" {
		baseURL = DefaultBaseURL
	}
	return &Client{BaseURL: strings.TrimRight(baseURL, "/"), JWT: jwt, HTTP: &http.Client{Timeout: 12 * time.Second}}
}

// APIError is a non-2xx answer.
type APIError struct {
	Status  int
	Message string
}

func (e *APIError) Error() string { return fmt.Sprintf("1click: %d %s", e.Status, e.Message) }

func (c *Client) do(ctx context.Context, method, path string, body any, out any) error {
	var rd io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		rd = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.BaseURL+path, rd)
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	if c.JWT != "" {
		req.Header.Set("Authorization", "Bearer "+c.JWT)
	}
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode/100 != 2 {
		var m struct {
			Message string `json:"message"`
		}
		_ = json.Unmarshal(raw, &m)
		if m.Message == "" {
			m.Message = http.StatusText(resp.StatusCode)
		}
		return &APIError{Status: resp.StatusCode, Message: m.Message}
	}
	return json.Unmarshal(raw, out)
}

// Quote calls POST /v0/quote.
func (c *Client) Quote(ctx context.Context, req QuoteRequest) (*QuoteResponse, error) {
	var out QuoteResponse
	if err := c.do(ctx, http.MethodPost, "/v0/quote", req, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// Status calls GET /v0/status.
func (c *Client) Status(ctx context.Context, depositAddress string) (string, error) {
	var out struct {
		Status string `json:"status"`
	}
	if err := c.do(ctx, http.MethodGet, "/v0/status?depositAddress="+depositAddress, nil, &out); err != nil {
		return "", err
	}
	return out.Status, nil
}

// DepositRequest builds the quote for an RSVP: exactly `microUSDC` must land
// as USDC on NEAR at recipient, paid from origin; refunds go back to refundTo.
func DepositRequest(o Origin, microUSDC uint64, refundTo, recipient string, dry bool, now time.Time) QuoteRequest {
	return QuoteRequest{
		Dry: dry, SwapType: "EXACT_OUTPUT", SlippageTolerance: 100,
		OriginAsset: o.AssetID, DepositType: "ORIGIN_CHAIN",
		DestinationAsset: USDCOnNear, Amount: strconv.FormatUint(microUSDC, 10),
		RefundTo: refundTo, RefundType: "ORIGIN_CHAIN",
		Recipient: recipient, RecipientType: "DESTINATION_CHAIN",
		Deadline: now.Add(30 * time.Minute).UTC().Format("2006-01-02T15:04:05.000Z"),
	}
}

// OfflineQuote computes a labelled stand-in quote from the snapshot price,
// for demo mode without network. Spread is a flat 1.2%.
func OfflineQuote(o Origin, req QuoteRequest) *QuoteResponse {
	out, _ := strconv.ParseUint(req.Amount, 10, 64)
	usd := float64(out) / 1e6
	in := usd * 1.012 / o.DemoPrice
	inUnits := in * math.Pow10(o.Decimals)
	prec := 6
	if o.Decimals < 6 {
		prec = o.Decimals
	}
	return &QuoteResponse{
		Quote: Quote{
			AmountIn:           strconv.FormatFloat(math.Ceil(inUnits), 'f', 0, 64),
			AmountInFormatted:  strconv.FormatFloat(in, 'f', prec, 64),
			AmountInUSD:        strconv.FormatFloat(usd*1.012, 'f', 2, 64),
			AmountOut:          req.Amount,
			AmountOutFormatted: strconv.FormatFloat(usd, 'f', 2, 64),
			TimeEstimate:       40,
		},
		QuoteRequest: req,
		Offline:      true,
	}
}

// WithFallback returns a Quoter that uses q and falls back to OfflineQuote
// when the API cannot be reached (network errors only, never API rejections).
func WithFallback(q Quoter) Quoter { return fallback{q} }

type fallback struct{ q Quoter }

func (f fallback) Quote(ctx context.Context, req QuoteRequest) (*QuoteResponse, error) {
	res, err := f.q.Quote(ctx, req)
	var apiErr *APIError
	if err != nil && !errors.As(err, &apiErr) {
		for _, o := range Origins {
			if o.AssetID == req.OriginAsset {
				return OfflineQuote(o, req), nil
			}
		}
	}
	return res, err
}

func (f fallback) Status(ctx context.Context, a string) (string, error) { return f.q.Status(ctx, a) }
