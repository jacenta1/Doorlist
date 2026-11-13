# You're invited to try Doorlist

**What:** RSVP to a meetup with a small deposit. Show up and get it back plus a share of the no-shows. Flake and your deposit goes to the people who came.
**When:** whenever you run `go run ./cmd/doorlist`
**Where:** http://localhost:8080 (no keys, no accounts, no wallet: it starts in a labelled demo mode)
**Dress code:** Go 1.25+. Nothing else for the app. Rust only if you want to run the contract's own tests.

Built at NEAR HACKATHON, track **Social Money**.

**The problem.** Free events fill up on paper and sit half empty on the night. Hosts over-invite to compensate, and the people who did come get a worse room. A deposit fixes that only if guests can pay it without friction (most don't hold NEAR) and if nobody has to trust the host with the money.

---

## For judges: the five-minute path

| Do | See |
|---|---|
| `go run ./cmd/doorlist`, open http://localhost:8080 | Five seeded events: one settled, one with doors open now, two upcoming |
| Open **Builders Night** → **Deposit & RSVP** → name, "USDC · Base", any `0x…` address → **Get my deposit address** | A live NEAR Intents 1Click quote (`dry: true`): the exact amount to send so that 10 USDC lands on NEAR |
| **Simulate the deposit (demo)** | Your ticket: a QR code, a six-letter code, and the money stages (Deposited → Checked in → Refunded + share) |
| **Host an event** → **Continue as demo host** → **Rooftop Demo Hour** → **Open the door scanner** | The phone makes its own ed25519 door key and scans tickets with the camera. No camera? Type `DOO-RDE`: "IN." |
| Back on the host page: **Close & pay out** | One DAO Transfer proposal per attendee, approved; the no-show row is struck through |
| Open http://localhost:8080/t/zk-reading-club/SHOWEDUPDEMOAAAA | "YOU SHOWED UP. +1.36 USDC" and a payout receipt |
| Open http://localhost:8080/break | 19 attacks on the ledger, run on page load: "19 of 19 refused" |
| `go test ./...` | 7 packages ok, 32 tests |
| `cd contract/rust && cargo test` | 12 passed (the deployable contract) |

Screenshots are in [`docs/screens/`](docs/screens). A narrated two-minute script is in [`docs/demo-script.md`](docs/demo-script.md).

---

## RSVP (as a guest)

1. Scan the event QR on the poster or open the link.
2. Deposit 10 USDC from any chain: USDC on Base, Arbitrum, Solana or Ethereum, USDT on Tron, ETH, SOL, BTC, or USDC from a NEAR account. A 1Click deposit address takes whatever you send and settles exactly the deposit as USDC on NEAR. You never need a NEAR wallet.
3. You're on the list. Keep the ticket link: it is your way in.

Your name is shown to the host and is never written on chain. On chain you are `base:0x…` (or your NEAR account), a ticket code and a deposit.

## At the door (as a host)

1. Open `/door` on your phone (from the host page). The phone generates a door key in the browser; you register it for the event once.
2. Scan the guest's QR: they're checked in, and the deposit is marked for refund. The check-in is signed by the door key, so the contract accepts it without a wallet popup per guest. Camera broken? Type the six-letter code.
3. Close the event: no-show deposits split equally among everyone who came, and payouts run on their own.

## The pot

- Deposits do not sit in the Doorlist contract. `ft_on_transfer` records the RSVP and forwards the USDC to the event's **Sputnik DAO** in the same transaction.
- The DAO's council is the hosts. The Doorlist contract holds one extra role, `doorlist`, that may only add and approve `Transfer` proposals. On close it files one proposal per attendee (`deposit + forfeit / shows`) and approves it. Every payout is a DAO proposal anyone can read.
- Integer division leaves at most `shows − 1` micro-USDC; that dust stays in the DAO. If nobody shows up, everything stays in the DAO for the council to decide.
- Example: 40 going at 10 USDC, 31 show. Forfeit 90 USDC. Each attendee gets 12.903225 USDC; 0.000025 stays in the DAO.

**Attendance is reputation.** Each check-in adds one to the guest's count in the contract (`get_shows`). An event can open early to people with N past shows ("Regulars first"). The count is not taken on faith: `GET /api/aura/<guest>` recomputes it from the RSVP history and reports whether it matches.

## For hosts: FAQ

**Do my guests need a wallet or NEAR?** No. They pay from the chain they already use and get paid back there.

**Do I?** Yes, a NEAR wallet. Sign-in is a NEP-413 signed message through NEAR Connect, with Meteor Wallet as the suggested wallet; any NEAR wallet in the connector works.

**Can I run off with the pot?** Not alone. It sits in a DAO whose council you choose; payouts that match the check-in list need no vote, anything else needs the council.

**What if my phone dies at the door?** Any phone can become a door: open the door page there and register it (up to 8 per event). The typed-code fallback works on any browser.

**A guest gave their ticket to a friend.** Fine. The ticket is the bearer; the payout still goes to the address that paid.

**What does it cost?** NEAR gas (fractions of a cent per check-in) and the 1Click swap fee the guest sees in the quote. Doorlist takes nothing.

**Running a real one?** [`kit/host-checklist.md`](kit/host-checklist.md) and the printable poster at `/e/<event>/poster`.

---

## Where it stands

| | |
|---|---|
| ✅ | Guest flow end to end in demo mode: quote → deposit (simulated) → ticket QR → check-in → close → payout receipt |
| ✅ | Ledger rules, tested: 32 Go tests, 12 Rust tests, a 19-attack suite |
| ✅ | Deployable Rust contract: builds to a 219 KB wasm with `cargo near build`, ABI generated |
| ✅ | Door scanner: in-browser ed25519 key, camera scan (native detector or bundled decoder), typed-code fallback |
| ✅ | Real 1Click prices in the RSVP flow (`dry: true`), with a labelled offline estimate when the API is unreachable |
| ✅ | Printable QR poster, host checklist, MCP server for agents, Dockerfile and Koyeb config |
| ⚠️ | Live mode (real contract over RPC, relayer transactions, wallet sign-in) is written and unit-tested in parts (transaction encoding, NEP-413) but **has never run against a deployed contract** |
| ⚠️ | The contract has not been run in a sandbox with a real Sputnik DAO and a real token; the DAO call shapes follow the Sputnik v2 interface and are checked only as JSON |
| ⚠️ | The near-sdk-go version of the contract type-checks but was not compiled to wasm (see [`contract/README.md`](contract/README.md)) |
| ❌ | No testnet or mainnet deployment, no real deposit, no real meetup yet |
| ❌ | Cross-chain payout forwarding (USDC out of the relayer's outbox back to the guest's chain) is designed, not built |
| ❌ | Invite codes, encrypted guest names inside invite links, passkey or email sign-in for guests |
| ❌ | The app keeps guest names and quotes in memory; a restart in live mode loses names (the ledger is unaffected) |

## What is real and what is simulated

| Piece | In the demo |
|---|---|
| Ledger rules (RSVP, check-in, close, split, payout state) | **Real code.** `contract/core`, the same state machine as the Rust contract, with real ed25519 signatures |
| 1Click quotes | **Real API**, `dry: true`: real prices, no deposit address, priced to a placeholder recipient |
| The chain, the USDC token, the Sputnik DAO | **Simulated in process.** Balances and proposals are bookkeeping in memory |
| The deposit arriving | **Simulated** by the "Simulate the deposit" button |
| Events, guests, names, amounts | **Seeded**, deterministic, invented. All accounts end in `.testnet` and do not exist |
| Host sign-in | **Skipped**: "Continue as demo host" |

No number in the demo is a usage figure. Doorlist has had no real users.

## What can go wrong

- **The relayer is trusted for cross-chain guests.** 1Click settles to the relayer, which then deposits into the contract on the guest's behalf, and their payout returns through the relayer's outbox. A dishonest relayer could keep a deposit before it reaches the contract. Guests paying from a NEAR account skip the relayer entirely.
- **The host is trusted at the door.** A host can check in friends who aren't there. Door signatures stop everyone else from faking a check-in, not the host. Mitigation: every check-in is public, with the door key that signed it.
- **One person, several addresses.** "One RSVP per address" does not stop someone booking twice from two wallets. They pay two deposits and can only show up once, so it costs them.
- **A lost ticket link is a lost ticket** until the host looks the guest up by code.
- **Payout proposals can fail** (DAO out of storage on the token, guest account not registered with USDC). The RSVP stays "paying" and the council can approve or retry in the DAO.
- **Clocks.** A door signature is accepted within 15 minutes of block time; a phone with a badly wrong clock is refused.
- **near-sdk-go** is a young community SDK; we did not deploy with it. Details in [`contract/README.md`](contract/README.md).

## Remove NEAR and this stops

- **NEAR Intents (1Click).** Without it every guest needs the right token on the right chain and a wallet for it. "Pay from wherever you are" is the whole RSVP.
- **A NEAR contract** holds the rules: who paid, which door keys count, who was checked in, what each person is owed. Native `ed25519_verify` makes a browser-generated door key enough to authorise a check-in.
- **Sputnik DAO** holds the money, so neither the app nor a single host does, and every payout is a public proposal.
- **Named accounts and NEP-413** give hosts a readable identity and a sign-in without a password.
- Without these, Doorlist is a spreadsheet and a payment link, and the host holds the cash.

## For developers

```
go test ./...                          → 7 packages ok, 32 tests
cd contract/rust && cargo test         → 12 passed
make wasm                              → contract/rust/target/near/doorlist_contract.wasm
```

```
cmd/doorlist/        the server (one binary; templates and assets embedded)
cmd/doorlist-mcp/    MCP server over stdio: list_events, event_pot, start_rsvp, ticket_status, aura
internal/event/      events, guests, the demo chain, the live chain, the break-it suite
internal/pot/        USDC maths, projections, the Sputnik DAO policy
internal/checkin/    tickets, QR payloads, door signatures
internal/intents/    NEAR Intents 1Click client
internal/near/       transaction signing, RPC, NEP-413 verification
contract/core/       the ledger in Go        contract/rust/  the deployable contract
web/templates/       templ views             web/static/     CSS, door scanner, vendored htmx and QR decoder
scripts/             testnet deploy, DAO creation, event creation
kit/                 host checklist for a real meetup
```

**Stack.** Go `net/http`, templ, htmx. No JS framework; the only scripts are the door scanner and the wallet sign-in. Hand-written CSS on a classless base.

**Going live (testnet).** `scripts/deploy-testnet.sh`, then `scripts/create-dao.sh`, then copy `.env.example` to `.env` and set `DOORLIST_MODE=live`. NEAR Intents has no testnet, so real cross-chain deposits (`DOORLIST_LIVE_QUOTES=1`) mean mainnet and small amounts.

**Hosting.** `Dockerfile` and `koyeb.yaml` are included; `/healthz` is the health check.

**Agents.** `DOORLIST_URL=http://localhost:8080 go run ./cmd/doorlist-mcp` speaks MCP on stdio.

### Built when, and with what

Everything in this repository was written during the hackathon window, 10–12 November 2026. There is no earlier code.

Third-party code: [htmx](https://htmx.org) 2.0.4 and [jsQR](https://github.com/cozmo/jsQR) 1.4.0 (vendored in `web/static/vendor`), [templ](https://templ.guide), [rsc.io/qr](https://pkg.go.dev/rsc.io/qr), near-sdk (Rust), near-sdk-go, NEAR Connect (loaded from a CDN in live mode only). Fonts: Anton and DM Sans.

AI tools: the code, tests and this README were written with Claude Code, directed and reviewed by the team.

License: [MIT](LICENSE).
