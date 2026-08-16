package main

import (
	"context"
	"errors"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/haochase/haowork/internal/demoserver"
)

func main() {
	address := os.Getenv("HAOWORK_DEMO_ADDR")
	if address == "" {
		address = "127.0.0.1:4175"
	}
	server := &http.Server{Addr: address, Handler: demoserver.NewHandler(), ReadHeaderTimeout: 5 * time.Second}
	shutdown, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	go func() {
		<-shutdown.Done()
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_ = server.Shutdown(ctx)
	}()
	log.Printf("Haowork read-only demo listening on %s", address)
	if err := server.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		log.Fatal(err)
	}
}
