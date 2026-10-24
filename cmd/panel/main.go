package main

import (
	"context"
	"log"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/tavvet/TunnelaPanel/internal/panel"
)

// Release packaging replaces version through the Go linker's -X flag.
var version = "dev"

func env(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

func main() {
	split := func(r rune) bool {
		return r == ',' || r == ' '
	}
	config := panel.Config{
		StateDir:       env("PANEL_STATE_DIR", "/data/panel"),
		Socket:         env("TT_SOCKET", "/run/tunnelapanel/control.sock"),
		SecureCookie:   os.Getenv("PANEL_COOKIE_SECURE") == "true",
		AllowedHosts:   strings.FieldsFunc(os.Getenv("PANEL_ALLOWED_HOSTS"), split),
		TrustedProxies: strings.FieldsFunc(os.Getenv("PANEL_TRUSTED_PROXIES"), split),
		Version:        version,
	}
	// Behind a reverse proxy without a trusted address, every client shares the
	// proxy's IP, so one client's failed logins would block everyone else.
	if len(config.AllowedHosts) > 0 && len(config.TrustedProxies) == 0 {
		log.Print("warning: PANEL_ALLOWED_HOSTS is set but PANEL_TRUSTED_PROXIES is empty; behind a reverse proxy all clients share one login rate limit")
	}

	app, err := panel.New(config)
	if err != nil {
		log.Fatal(err)
	}
	srv := &http.Server{
		Addr:              env("PANEL_LISTEN", ":8080"),
		Handler:           app,
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       15 * time.Second,
		// Allow the agent client's 70-second timeout to finish before the response
		// deadline; applying settings can include a restart and rollback.
		WriteTimeout:   90 * time.Second,
		IdleTimeout:    60 * time.Second,
		MaxHeaderBytes: 16 << 10,
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	go func() {
		<-ctx.Done()
		timeout, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_ = srv.Shutdown(timeout)
	}()

	log.Printf("TunnelaPanel %s listening on %s; bootstrap credentials are stored in PANEL_STATE_DIR/bootstrap-password", version, srv.Addr)
	if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		log.Fatal(err)
	}
}
