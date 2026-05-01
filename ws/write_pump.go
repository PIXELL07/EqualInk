package ws

import (
	"time"

	"github.com/gorilla/websocket"
	"github.com/pixell07/equalink/hub"
)

// WritePump drains the client Send channel → pushes to WebSocket.
// Sends pings to detect dead connections.
func WritePump(client *hub.Client, conn *websocket.Conn) {
	ticker := time.NewTicker(PingPeriod)
	defer func() {
		ticker.Stop()
		conn.Close()
	}()

	for {
		select {
		case msg, ok := <-client.Send:
			conn.SetWriteDeadline(time.Now().Add(WriteWait))
			if !ok {
				conn.WriteMessage(websocket.CloseMessage, []byte{})
				return
			}
			conn.WriteMessage(websocket.BinaryMessage, msg)
		case <-ticker.C:
			conn.SetWriteDeadline(time.Now().Add(WriteWait))
			if err := conn.WriteMessage(websocket.PingMessage, nil); err != nil {
				return
			}
		}
	}
}
