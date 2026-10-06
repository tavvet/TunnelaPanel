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

var version = "dev"

func env(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

func main() {
	app, err := panel.New(panel.Config{StateDir: env("PANEL_STATE_DIR", "/data/panel"), Socket: env("TT_SOCKET", "/run/tunnelapanel/control.sock"), SecureCookie: os.Getenv("PANEL_COOKIE_SECURE") == "true", AllowedHosts: strings.FieldsFunc(os.Getenv("PANEL_ALLOWED_HOSTS"), func(r rune) bool { return r == ',' || r == ' ' }), Version: version})
	if err != nil {
		log.Fatal(err)
	}
	srv := &http.Server{Addr: env("PANEL_LISTEN", ":8080"), Handler: app, ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 15 * time.Second, WriteTimeout: 90 * time.Second, IdleTimeout: 60 * time.Second, MaxHeaderBytes: 16 << 10}
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
