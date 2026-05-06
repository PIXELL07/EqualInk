package api

/*

  HTTP → WebSocket Upgrade

  THIS IS THE ENTRY POINT for every collaborative edit.

  FLOW on GET /ws?token=JWT&doc=UUID&sv=BASE64:
  1. Validate JWT from query param
     WHY query param not header?
     WebSocket upgrade requests are plain HTTP GET.
     The browser WebSocket API cannot set custom headers.
     Query param is the standard workaround.
  2. Check user has access to this doc
  3. Upgrade HTTP → WebSocket
  4. Create Client, Register with Hub
  5. Send current doc state (OnJoin)
  6. Spawn WritePump goroutine
  7. THIS goroutine becomes ReadPump (blocks forever)

*/

import (
	"encoding/base64"
	"log"
	"net/http"

	"github.com/pixell07/equalink/auth"
	"github.com/pixell07/equalink/hub"
	appSync "github.com/pixell07/equalink/sync"
	"github.com/pixell07/equalink/ws"
)

// JWTConfig is satisfied by config.Config
type JWTConfig interface {
	GetJWTSecret() string
}

// WSHandler returns the HTTP handler that upgrades to WebSocket.
func WSHandler(h *hub.Hub, syncHandler *appSync.Handler, session *appSync.Session, cfg JWTConfig) http.HandlerFunc {
	upgrader := ws.NewUpgrader(nil) // nil = allow all origins in dev

	return func(w http.ResponseWriter, r *http.Request) {
		// 1. Validate JWT from ?token= query param
		tokenStr := r.URL.Query().Get("token")
		if tokenStr == "" {
			http.Error(w, "missing token", http.StatusUnauthorized)
			return
		}
		userID, err := auth.ValidateJWT(tokenStr, cfg.GetJWTSecret())
		if err != nil {
			http.Error(w, "invalid token", http.StatusUnauthorized)
			return
		}

		// 2. Validate doc ID
		docID := r.URL.Query().Get("doc")
		if docID == "" {
			http.Error(w, "doc param required", http.StatusBadRequest)
			return
		}

		// 3. Check doc access (user must be a member)
		if err := syncHandler.Validator.CheckDocAccess(docID, userID); err != nil {
			http.Error(w, "access denied", http.StatusForbidden)
			return
		}

		// 4. Upgrade HTTP → WebSocket
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			log.Printf("[ws] upgrade failed: user=%s err=%v", userID, err)
			return // gorilla writes the error response
		}

		// 5. Create and register client
		client := hub.NewClient(userID, docID)
		h.Register <- client

		// 6. Send current doc state to new joiner
		// Decode client's Yjs state vector if provided (for efficient diff)
		var stateVector []byte
		if svB64 := r.URL.Query().Get("sv"); svB64 != "" {
			stateVector, _ = base64.StdEncoding.DecodeString(svB64)
		}
		if err := session.OnJoin(client, stateVector); err != nil {
			log.Printf("[ws] OnJoin error: user=%s doc=%s err=%v", userID, docID, err)
		}

		// 7. Spawn WritePump in its own goroutine
		go ws.WritePump(client, conn)

		// This goroutine becomes ReadPump — blocks until disconnect
		ws.ReadPump(client, conn, h, syncHandler)
	}
}
