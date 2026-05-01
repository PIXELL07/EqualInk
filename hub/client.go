package hub

// Client represents one connected browser tab.
type Client struct {
	UserID string
	DocID  string
	Send   chan []byte
}
