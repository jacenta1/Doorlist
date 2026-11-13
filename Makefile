export PATH := /opt/homebrew/opt/rustup/bin:$(HOME)/.cargo/bin:$(PATH)

run:            ## demo mode on :8080
	go run ./cmd/doorlist
test:           ## Go tests (app + contract logic + break-it suite)
	go test ./...
test-contract:  ## Rust contract unit tests
	cd contract/rust && cargo test
test-all: test test-contract
generate:       ## regenerate templ views after editing *.templ
	go tool templ generate
wasm:           ## build the deployable contract
	cd contract/rust && cargo near build non-reproducible-wasm
.PHONY: verify testnet-run run test test-contract test-all generate wasm
