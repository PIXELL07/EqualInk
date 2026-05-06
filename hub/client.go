package hub

// Client represents one active browser tab connected via WebSocket.
//
// WHY a buffered Send channel (not a direct conn write)?
// gorilla/websocket is NOT safe for concurrent writes.
// If Hub and a ping ticker both tried to write to conn
// simultaneously, you'd get a race condition and panic.
//
// Solution: all writes go into the Send channel.
// WritePump is the ONLY goroutine that calls conn.WriteMessage.
// It drains Send at its own pace — serialised, safe.
//
// Buffer size 256:
// At 100 bytes/message, 256 messages = 25KB.
// A slow client can fall 256 messages behind before being evicted.
// This gives ~5-10 seconds of buffer on a slow 3G connection.
type Client struct {
	UserID string
	DocID  string
	Send   chan []byte // buffered; WritePump drains it
}

// NewClient creates a client with a buffered channel.
func NewClient(userID, docID string) *Client {
	return &Client{
		UserID: userID,
		DocID:  docID,
		Send:   make(chan []byte, 256),
	}
}
