package near

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"sync"
	"time"

	"doorlist/contract/core"
)

// RPC is a minimal NEAR JSON-RPC client.
type RPC struct {
	URL  string
	HTTP *http.Client
}

// NewRPC returns a client for url.
func NewRPC(url string) *RPC {
	return &RPC{URL: url, HTTP: &http.Client{Timeout: 20 * time.Second}}
}

type rpcErr struct {
	Name  string          `json:"name"`
	Cause json.RawMessage `json:"cause"`
	Data  json.RawMessage `json:"data"`
}

// Call runs one JSON-RPC method.
func (r *RPC) Call(ctx context.Context, method string, params any, out any) error {
	body, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": "doorlist", "method": method, "params": params})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, r.URL, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := r.HTTP.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	var env struct {
		Result json.RawMessage `json:"result"`
		Error  *rpcErr         `json:"error"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&env); err != nil {
		return err
	}
	if env.Error != nil {
		return fmt.Errorf("near rpc %s: %s %s", method, env.Error.Name, string(env.Error.Cause)+string(env.Error.Data))
	}
	if out != nil {
		return json.Unmarshal(env.Result, out)
	}
	return nil
}

// View calls a view method and decodes its JSON result into out.
func (r *RPC) View(ctx context.Context, contract, method string, args any, out any) error {
	a, _ := json.Marshal(args)
	var res struct {
		Result []byte `json:"result"`
		Error  string `json:"error"`
	}
	err := r.Call(ctx, "query", map[string]any{
		"request_type": "call_function", "finality": "final",
		"account_id": contract, "method_name": method, "args_base64": base64.StdEncoding.EncodeToString(a),
	}, &res)
	if err != nil {
		return err
	}
	if res.Error != "" {
		return errors.New(res.Error)
	}
	return json.Unmarshal(res.Result, out)
}

// AccessKey returns the key's nonce and permission ("FullAccess" or a function-call object).
func (r *RPC) AccessKey(ctx context.Context, account, pubKey string) (nonce uint64, fullAccess bool, err error) {
	var res struct {
		Nonce      uint64          `json:"nonce"`
		Permission json.RawMessage `json:"permission"`
		Error      string          `json:"error"`
	}
	err = r.Call(ctx, "query", map[string]any{
		"request_type": "view_access_key", "finality": "final", "account_id": account, "public_key": pubKey,
	}, &res)
	if err != nil {
		return 0, false, err
	}
	if res.Error != "" {
		return 0, false, errors.New(res.Error)
	}
	return res.Nonce, string(res.Permission) == `"FullAccess"`, nil
}

// LatestBlockHash returns the final block hash.
func (r *RPC) LatestBlockHash(ctx context.Context) ([32]byte, error) {
	var h [32]byte
	var res struct {
		Header struct {
			Hash string `json:"hash"`
		} `json:"header"`
	}
	if err := r.Call(ctx, "block", map[string]any{"finality": "final"}, &res); err != nil {
		return h, err
	}
	b, err := core.Base58Decode(res.Header.Hash)
	if err != nil || len(b) != 32 {
		return h, errors.New("near: bad block hash")
	}
	copy(h[:], b)
	return h, nil
}

// Signer sends function calls from one account with one key.
type Signer struct {
	RPC     *RPC
	Account string
	Key     ed25519.PrivateKey
	mu      sync.Mutex
	nonce   uint64
}

// Outcome is the part of a transaction result the app reads.
type Outcome struct {
	Hash            string          `json:"hash"`
	SuccessJSON     json.RawMessage `json:"success_value"`
	Failure         string          `json:"failure,omitempty"`
	Logs            []string        `json:"logs,omitempty"`
	ReceiptFailures []string        `json:"receipt_failures,omitempty"`
}

// Send signs and sends one transaction and waits for the final result.
func (s *Signer) Send(ctx context.Context, receiver string, calls ...FunctionCall) (*Outcome, error) {
	acts := make([]Action, len(calls))
	for i, c := range calls {
		acts[i] = c
	}
	return s.SendActions(ctx, receiver, acts...)
}

// SendActions signs and sends any actions and waits for the final result.
func (s *Signer) SendActions(ctx context.Context, receiver string, actions ...Action) (*Outcome, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	pub := s.Key.Public().(ed25519.PublicKey)
	if s.nonce == 0 {
		n, _, err := s.RPC.AccessKey(ctx, s.Account, PublicKeyString(pub))
		if err != nil {
			return nil, err
		}
		s.nonce = n
	}
	bh, err := s.RPC.LatestBlockHash(ctx)
	if err != nil {
		return nil, err
	}
	s.nonce++
	tx := &Transaction{SignerID: s.Account, PublicKey: pub, Nonce: s.nonce, ReceiverID: receiver, BlockHash: bh, Actions: actions}
	signed, hash := tx.Sign(s.Key)
	var res struct {
		ReceiptsOutcome []struct {
			Outcome struct {
				Logs   []string        `json:"logs"`
				Status json.RawMessage `json:"status"`
			} `json:"outcome"`
		} `json:"receipts_outcome"`
		Status struct {
			SuccessValue *string         `json:"SuccessValue"`
			Failure      json.RawMessage `json:"Failure"`
		} `json:"status"`
	}
	err = s.RPC.Call(ctx, "send_tx", map[string]any{
		"signed_tx_base64": base64.StdEncoding.EncodeToString(signed), "wait_until": "FINAL",
	}, &res)
	out := &Outcome{Hash: core.Base58Encode(hash[:])}
	if err != nil {
		s.nonce = 0 // re-read on next send
		return out, err
	}
	for _, r := range res.ReceiptsOutcome {
		out.Logs = append(out.Logs, r.Outcome.Logs...)
		if bytes.Contains(r.Outcome.Status, []byte("Failure")) {
			out.ReceiptFailures = append(out.ReceiptFailures, string(r.Outcome.Status))
		}
	}
	if res.Status.Failure != nil {
		out.Failure = string(res.Status.Failure)
		return out, errors.New("near: transaction failed: " + out.Failure)
	}
	if res.Status.SuccessValue != nil {
		out.SuccessJSON, _ = base64.StdEncoding.DecodeString(*res.Status.SuccessValue)
	}
	return out, nil
}
