package main

import (
	"context"
	"log"
	"os"
	"os/signal"
	"syscall"

	"github.com/tavvet/TunnelaPanel/internal/endpoint"
)

func main() {
	if err := run(); err != nil {
		log.Print(err)
		os.Exit(1)
	}
}

func run() error {
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()

	agent, err := endpoint.New(endpoint.ConfigFromEnv())
	if err != nil {
		return err
	}
	defer agent.Close()
	if err = agent.Start(ctx); err != nil {
		return err
	}
	// Certbot signals the agent, which validates the replacement certificate and
	// forwards SIGHUP to TrustTunnel without restarting it or applying the draft.
	hup := make(chan os.Signal, 1)
	signal.Notify(hup, syscall.SIGHUP)
	defer signal.Stop(hup)
	go func() {
		for {
			select {
			case <-ctx.Done():
				return
			case <-hup:
				if err := agent.ReloadTLS(); err != nil {
					log.Print(err)
				}
			}
		}
	}()

	return agent.Serve(ctx)
}
