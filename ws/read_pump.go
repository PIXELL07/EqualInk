package ws

import (
	"github.com/gorilla/websocket"
	"github.com/pixell07/equalink/hub"
	"github.com/pixell07/equalink/sync"
)

// ReadPump listens for Yjs binary diffs from ONE client.
// Runs in its own goroutine. Cleans up on disconnect.
func ReadPump(client *hub.Client, conn *websocket.Conn, h *hub.Hub, handler *sync.Handler) {
	defer func() {
		h.Unregister <- client
		conn.Close()
	}()

	conn.SetReadLimit(MaxMessageSize)

	for {
		_, payload, err := conn.ReadMessage()
		if err != nil {
			break
		}
		handler.HandleUpdate(client.UserID, client.DocID, payload, h)
	}
}
