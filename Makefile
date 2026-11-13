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
verify:         ## re-read the real testnet event and check every number (no keys)
	go run ./cmd/doorctl verify
testnet-run:    ## run the testnet event again (needs the master key in ~/.near-credentials)
	go run ./cmd/doorctl run
names:          ## no design-reference or rival names in the repo (CSS keywords like "linear" excepted)
	@! grep -rnIiE "ethena|\breflect\b|granola|stripe|\bexa\b|\bfinch\b|works in progress|polymarket|mschf|zora|\bluma\b|partiful|showup|pull up" --exclude-dir=.git --exclude-dir=target --exclude-dir=vendor --exclude=Makefile --exclude=go.sum . || (echo "forbidden name found" && exit 1)
	@! grep -rnIiE "\blinear\b" --include=*.go --include=*.templ --include=*.md --include=*.rs --include=*.js --exclude-dir=.git --exclude-dir=target --exclude-dir=vendor . || (echo "forbidden name found" && exit 1)
.PHONY: verify testnet-run names run test test-contract test-all generate wasm
