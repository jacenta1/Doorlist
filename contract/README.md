# The Doorlist contract

Three directories, one state machine.

| Path | What | Status |
|---|---|---|
| `core/` | The ledger in plain Go: events, RSVPs, door-signed check-ins, close, split, DAO payout calls. No NEAR imports. | Tested by `go test ./...`. The web app runs this in demo mode. |
| `rust/` | The same ledger with Rust near-sdk 5.30. **This is the contract to deploy.** | `cargo test`: 12 passed. Builds to a 219 KB wasm with `cargo near build`. |
| `near-sdk-go/` | A binding of `core/` to the community near-sdk-go SDK (v0.1.1). | Type-checks against the SDK. **Not built to wasm and not deployed.** |

## Why Rust is the deployed contract

The plan was near-sdk-go. What day 1 found:

1. near-sdk-go only compiles with TinyGo plus its own `near-go` CLI, which generates the exported functions. Neither was available in the build environment, so a wasm could not be produced or run in a sandbox. Shipping a contract that holds deposits without ever executing its wasm was not acceptable.
2. Reading the SDK's code generator showed a real hazard for escrow: a method's return value is JSON-encoded by the generated wrapper and then encoded again by `ReturnValue`. NEP-141 tokens read `ft_on_transfer`'s result as a bare number string; a double-encoded `"0"` does not parse, and the token would refund every deposit while the ledger recorded an RSVP. The binding here avoids it by writing results itself, but it shows the SDK is young where money is involved.
3. Its mock `Ed25519Verify` always returns true, so door-signature checks cannot be unit-tested through the SDK. `core/` takes the verifier as an interface and is tested with real signatures instead.

So the Go logic was kept (it is what the Go server needs anyway), and the on-chain copy is Rust. The two are held together by shared fixtures: `testdata/split-vectors.json` is asserted by both test suites, and the check-in message format and base58 vector are asserted on both sides.

To try the Go binding: install TinyGo and `near-go`, then `cd contract/near-sdk-go && near-go build`.

## Methods

`init` · `create_event` · `add_door_key` · `remove_door_key` · `cancel_event` · `ft_on_transfer` · `check_in` · `close_event` · `payout_batch` · views `get_event`, `get_rsvp`, `list_rsvps`, `get_shows`, `can_rsvp`, `get_split`, `get_config`.

A check-in is valid when a door key the host registered signed `doorlist:checkin:v1|<event>|<ticket>|<unix ms>` within 15 minutes of block time, inside the check-in window. Anyone may submit it.
