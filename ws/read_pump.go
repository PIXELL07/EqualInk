package ws

/*
  Browser → Server                                                                         ║
  ONE goroutine per connected client. Blocks on
  conn.ReadMessage() — waiting for Yjs binary diffs.

  LIFECYCLE:
  1. Spawned by WSHandler after upgrade + registration
  2. Sets read limits and pong handler
  3. Loops forever reading binary frames
  4. Passes each frame to sync.Handler.HandleUpdate
  5. On ANY error (disconnect, timeout, close):
     defer fires → Unregister client → close conn

  WHY defer for cleanup?
  ReadPump can exit via 5 different code paths:
  normal close, abnormal close, read deadline, max size,
  or a panic. defer runs on ALL of them — no leaks.

*/

import (
	"log"
	"time"

	"github.com/gorilla/websocket"

	"github.com/pixell07/equalink/hub"
)

// UpdateHandler is the function signature sync.Handler.HandleUpdate satisfies.
// Using an interface avoids importing sync → ws (circular dependency).
type UpdateHandler interface {
	HandleUpdate(userID, docID string, payload []byte)
}

// ReadPump reads binary Yjs diffs from one WebSocket connection.
// Runs as its own goroutine. Cleans up on any exit.

func ReadPump(client *hub.Client, conn *websocket.Conn, h *hub.Hub, handler UpdateHandler) {
	defer func() {
		// Unregister fires regardless of HOW ReadPump exits
		h.Unregister <- client
		conn.Close()
		log.Printf("[ws] ReadPump exited: user=%s doc=%s", client.UserID, client.DocID)
	}()

	// Apply safety limits
	conn.SetReadLimit(MaxMessageSize)
	conn.SetReadDeadline(time.Now().Add(PongWait))

	// Reset read deadline every time we get a pong (proof of life)
	conn.SetPongHandler(func(appData string) error {
		conn.SetReadDeadline(time.Now().Add(PongWait))
		return nil
	})
	for {
		// Blocks here until a frame arrives, deadline fires, or conn closes
		messageType, payload, err := conn.ReadMessage()
		if err != nil {
			// IsUnexpectedCloseError: client closed browser tab, network loss, etc.
			// These are not bugs — don't log as errors
			if websocket.IsUnexpectedCloseError(err,
				websocket.CloseGoingAway,
				websocket.CloseAbnormalClosure,
				websocket.CloseNormalClosure,
			) {
				log.Printf("[ws] unexpected close: user=%s err=%v", client.UserID, err)
			}
			break // defer handles cleanup
		}
		// We only process binary messages (Yjs diffs are binary)
		// Text frames could be JSON control messages (future: task events)
		if messageType != websocket.BinaryMessage {
			continue
		}
		// Hand off to sync handler — ReadPump's ONLY job is reading and passing on
		handler.HandleUpdate(client.UserID, client.DocID, payload)
	}

}
