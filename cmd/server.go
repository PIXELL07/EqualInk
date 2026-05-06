package cmd

/*

  — Graceful Shutdown + Signal Handling

  HOW IT WORKS:
  Go's os/signal package lets us intercept SIGTERM
  (sent by Docker/k8s on pod stop) and SIGINT (Ctrl+C).
  Without this: process dies instantly, in-flight
  requests get a broken pipe, and analytics RAM buffer
  is lost forever.

  WITH this: we call srv.Shutdown(ctx) which:
  1. Stops accepting new connections immediately
  2. Waits up to 15s for active requests to finish
  3. Then we manually flush analytics + close DB pool

*/

import (
	"context"
	"database/sql"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/pixell07/equalink/analytics"
	"github.com/pixell07/equalink/document"
)

// GracefulServer wraps http.Server with shutdown dependencies.
type GracefulServer struct {
	HTTP    *http.Server
	DB      *sql.DB
	Tracker *analytics.Tracker
	Store   *document.Store
}

// Run starts the server and blocks until a signal is received.
// Call this from main.go instead of srv.ListenAndServe directly.
func (g *GracefulServer) Run() {
	// Start server in background goroutine so we can listen for signals
	errCh := make(chan error, 1)
	go func() {
		log.Printf("[server] EqualInk listening on %s", g.HTTP.Addr)
		if err := g.HTTP.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			errCh <- err
		}
	}()

	// Block until OS signal or server error
	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGTERM, syscall.SIGINT)

	select {
	case sig := <-quit:
		log.Printf("[server] Signal %v received — starting graceful shutdown", sig)
	case err := <-errCh:
		log.Printf("[server] Server error: %v — shutting down", err)
	}

	g.shutdown()
}

func (g *GracefulServer) shutdown() {
	// Step 1: Stop accepting new HTTP connections, drain existing ones (15s max)
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	if err := g.HTTP.Shutdown(ctx); err != nil {
		log.Printf("[server] HTTP shutdown error: %v", err)
	}
	log.Println("[server] HTTP server stopped")

	// Step 2: Force-flush analytics buffer so no data is lost on deploy
	// WHY: flusher ticks every 30s. Up to 30s of edits could be in RAM.
	// ForceFlush drains it to DB right now before the DB pool closes.
	log.Println("[server] Flushing analytics buffer...")
	g.Tracker.ForceFlush(func(docID, userID string, a *analytics.Activity, windowEnd time.Time) {
		if err := g.Store.SaveContribution(docID, userID, a, windowEnd); err != nil {
			log.Printf("[server] flush error for doc=%s user=%s: %v", docID, userID, err)
		}
	})

	// Step 3: Close database connection pool cleanly
	if err := g.DB.Close(); err != nil {
		log.Printf("[server] DB close error: %v", err)
	}
	log.Println("[server] EqualInk shut down cleanly.")
}
