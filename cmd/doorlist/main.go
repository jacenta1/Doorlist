// Command doorlist serves the Doorlist web app.
//
// With no configuration it runs in demo mode: seeded events, the real
// contract logic on a simulated chain and DAO, dry-run 1Click quotes.
package main

import (
	"context"
	"crypto/rand"
	"flag"
	"log"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"doorlist/internal/event"
	"doorlist/internal/intents"
	"doorlist/internal/near"
	"doorlist/internal/web"
)

func env(k, def string) string {
	if v := strings.TrimSpace(os.Getenv(k)); v != "" {
		return v
	}
	return def
}

func main() {
	addr := flag.String("addr", ":"+env("PORT", "8080"), "listen address")
	flag.Parse()

	mode := env("DOORLIST_MODE", "demo")
	base := strings.TrimRight(env("DOORLIST_BASE_URL", "http://localhost:"+env("PORT", "8080")), "/")
	network := env("NEAR_NETWORK", "testnet")
	rpcURL := env("NEAR_RPC_URL", "https://rpc."+network+".near.org")
	contract := env("DOORLIST_CONTRACT", "")
	loc, err := time.LoadLocation(env("DOORLIST_TZ", "UTC"))
	if err != nil {
		loc = time.UTC
	}

	secret := []byte(os.Getenv("DOORLIST_SECRET"))
	if len(secret) < 16 {
		secret = make([]byte, 32)
		_, _ = rand.Read(secret)
		if mode != "demo" {
			log.Print("DOORLIST_SECRET not set: host sessions reset on restart")
		}
	}

	quoter := intents.WithFallback(intents.NewClient(env("ONECLICK_BASE_URL", intents.DefaultBaseURL), os.Getenv("ONECLICK_JWT")))
	now := time.Now

	var svc *event.Service
	var rpc *near.RPC
	demo := mode == "demo"
	if demo {
		chain := event.NewSimChain(event.DemoConfig, now)
		svc = event.NewService(chain, quoter, now, base, true)
		if err := event.Seed(svc, chain); err != nil {
			log.Fatalf("seed: %v", err)
		}
		contract = "doorlist-demo.testnet"
	} else {
		if contract == "" {
			log.Fatal("live mode needs DOORLIST_CONTRACT (see .env.example)")
		}
		rpc = near.NewRPC(rpcURL)
		var signer *near.Signer
		if k := os.Getenv("DOORLIST_RELAYER_KEY"); k != "" {
			priv, err := near.ParsePrivateKey(k)
			if err != nil {
				log.Fatalf("DOORLIST_RELAYER_KEY: %v", err)
			}
			signer = &near.Signer{RPC: rpc, Account: env("DOORLIST_RELAYER_ACCOUNT", ""), Key: priv}
		}
		chain, err := event.NewLiveChain(rpc, contract, signer)
		if err != nil {
			log.Fatalf("contract %s: %v", contract, err)
		}
		svc = event.NewService(chain, quoter, now, base, false)
		svc.LiveQuotes = env("DOORLIST_LIVE_QUOTES", "") == "1"
		go settleLoop(svc)
	}

	srv := &web.Server{Svc: svc, Secret: secret, Demo: demo, Network: network, Contract: contract, RPC: rpc, Location: loc}
	hs := &http.Server{Addr: *addr, Handler: logRequests(srv.Handler()), ReadHeaderTimeout: 10 * time.Second}

	go func() {
		log.Printf("doorlist %s mode on %s (ledger: %s)", mode, base, svc.Chain.Name())
		if err := hs.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Fatal(err)
		}
	}()
	stop := make(chan os.Signal, 1)
	signal.Notify(stop, os.Interrupt, syscall.SIGTERM)
	<-stop
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_ = hs.Shutdown(ctx)
}

// settleLoop watches 1Click deposit addresses in live mode and puts each RSVP
// on chain once its deposit has settled as USDC on NEAR.
func settleLoop(svc *event.Service) {
	for range time.Tick(15 * time.Second) {
		for _, g := range svc.PendingDeposits() {
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			st, err := svc.Quoter.Status(ctx, g.Quote.Quote.DepositAddress)
			cancel()
			if err == nil && st == "SUCCESS" {
				if err := svc.Settle(g.Ticket); err != nil {
					log.Printf("settle %s: %v", g.Ticket, err)
				}
			}
		}
	}
}

func logRequests(h http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t := time.Now()
		h.ServeHTTP(w, r)
		if !strings.HasPrefix(r.URL.Path, "/static/") {
			log.Printf("%s %s %s", r.Method, r.URL.Path, time.Since(t).Round(time.Millisecond))
		}
	})
}
