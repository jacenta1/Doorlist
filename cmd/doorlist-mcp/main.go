// Command doorlist-mcp is a Model Context Protocol server (stdio, JSON-RPC
// 2.0, one message per line) over a running Doorlist app. It lets an agent
// list events, read an event's pot, start an RSVP and check a ticket.
//
//	DOORLIST_URL=http://localhost:8080 go run ./cmd/doorlist-mcp
package main

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"
)

type req struct {
	ID     json.RawMessage `json:"id"`
	Method string          `json:"method"`
	Params json.RawMessage `json:"params"`
}

type tool struct {
	Name        string         `json:"name"`
	Description string         `json:"description"`
	InputSchema map[string]any `json:"inputSchema"`
}

func obj(required []string, props map[string]string) map[string]any {
	p := map[string]any{}
	for k, d := range props {
		p[k] = map[string]string{"type": "string", "description": d}
	}
	return map[string]any{"type": "object", "properties": p, "required": required}
}

var tools = []tool{
	{"list_events", "List Doorlist events with phase, deposit, who is going and the pot.", obj(nil, nil)},
	{"event_pot", "One event's pot: deposits, check-ins, the DAO that holds it and what each attendee would get if it closed now.", obj([]string{"event"}, map[string]string{"event": "event id"})},
	{"start_rsvp", "Start an RSVP: returns the amount to send (a NEAR Intents 1Click quote) and the ticket link. Does not move money.", obj([]string{"event", "name", "origin", "address"}, map[string]string{
		"event": "event id", "name": "name shown to the host", "origin": "usdc-base, usdc-arb, usdc-sol, usdc-eth, usdt-tron, eth-base, eth-eth, sol-sol, btc-btc or near", "address": "the guest's address on that chain (refunds and payout)"})},
	{"ticket_status", "Whether a ticket's deposit is on chain, checked in, and paid.", obj([]string{"event", "ticket"}, map[string]string{"event": "event id", "ticket": "ticket code"})},
	{"aura", "A guest's attendance record recomputed from RSVP history, compared with the ledger's counter.", obj([]string{"guest"}, map[string]string{"guest": "NEAR account or chain:address"})},
}

var base = strings.TrimRight(envOr("DOORLIST_URL", "http://localhost:8080"), "/")
var client = &http.Client{Timeout: 25 * time.Second}

func envOr(k, d string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return d
}

func call(name string, a map[string]string) (string, error) {
	var resp *http.Response
	var err error
	switch name {
	case "list_events":
		resp, err = client.Get(base + "/api/events")
	case "event_pot":
		resp, err = client.Get(base + "/api/events/" + a["event"])
	case "ticket_status":
		resp, err = client.Get(base + "/api/tickets/" + a["event"] + "/" + a["ticket"])
	case "aura":
		resp, err = client.Get(base + "/api/aura/" + a["guest"])
	case "start_rsvp":
		b, _ := json.Marshal(map[string]string{"name": a["name"], "origin": a["origin"], "address": a["address"]})
		resp, err = client.Post(base+"/api/events/"+a["event"]+"/rsvp", "application/json", bytes.NewReader(b))
	default:
		return "", fmt.Errorf("unknown tool %q", name)
	}
	if err != nil {
		return "", fmt.Errorf("Doorlist app not reachable at %s: %w", base, err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode >= 400 {
		return "", fmt.Errorf("%s", strings.TrimSpace(string(body)))
	}
	return string(body), nil
}

func handle(r req) any {
	switch r.Method {
	case "initialize":
		return map[string]any{"protocolVersion": "2024-11-05", "capabilities": map[string]any{"tools": map[string]any{}}, "serverInfo": map[string]string{"name": "doorlist", "version": "0.1.0"}}
	case "tools/list":
		return map[string]any{"tools": tools}
	case "tools/call":
		var p struct {
			Name      string            `json:"name"`
			Arguments map[string]string `json:"arguments"`
		}
		_ = json.Unmarshal(r.Params, &p)
		text, err := call(p.Name, p.Arguments)
		if err != nil {
			return map[string]any{"isError": true, "content": []map[string]string{{"type": "text", "text": err.Error()}}}
		}
		return map[string]any{"content": []map[string]string{{"type": "text", "text": text}}}
	case "ping":
		return map[string]any{}
	}
	return nil
}

func main() {
	in := bufio.NewScanner(os.Stdin)
	in.Buffer(make([]byte, 1<<20), 1<<20)
	out := json.NewEncoder(os.Stdout)
	for in.Scan() {
		var r req
		if json.Unmarshal(in.Bytes(), &r) != nil || r.ID == nil {
			continue // notifications need no reply
		}
		res := handle(r)
		if res == nil {
			_ = out.Encode(map[string]any{"jsonrpc": "2.0", "id": r.ID, "error": map[string]any{"code": -32601, "message": "method not found"}})
			continue
		}
		_ = out.Encode(map[string]any{"jsonrpc": "2.0", "id": r.ID, "result": res})
	}
}
