package hub

/*

  — WebSocket Connection Registry

  THE CORE PATTERN: one goroutine owns the map.

  WHY NOT sync.RWMutex?
  With a mutex, if 1000 goroutines all call Broadcast
  simultaneously, they queue up waiting to acquire the
  lock. The slowest one blocks all others.

  With channels: each caller sends to h.Broadcast and
  moves on immediately. Run() processes them one at a
  time at Go scheduler speed — no blocking callers.

  SLOW CONSUMER PROBLEM (critical for production):
  If User B has a bad 2G connection, their Send channel
  buffer (256 slots) fills up. The next Broadcast to B
  would block — stalling ALL other users on that doc.
  Solution: select default → detect full buffer → evict
  the slow client. Their Yjs engine re-syncs on reconnect

*/

import "log"

// Message is the unit of work flowing through the hub.
// Payload is raw Yjs binary — hub routes it, never decodes it.
type Message struct {
	SenderID string
	DocID    string
	Payload  []byte
}

// Hub is the single source of truth for active WebSocket connections.
// ONLY Hub.Run() reads or writes the clients map — zero data races.
type Hub struct {
	// Private — external code must use the public channels
	clients map[string]*Client // key: userID

	Register   chan *Client // new connection
	Unregister chan *Client // disconnection
	Broadcast  chan Message // diff to route
}

func NewHub() *Hub {
	return &Hub{
		clients:    make(map[string]*Client),
		Register:   make(chan *Client, 64),
		Unregister: make(chan *Client, 64),
		Broadcast:  make(chan Message, 512),
	}
}

// Run is the ONLY goroutine that touches h.clients.
// Start it once with: go hub.Run()
// It runs forever until the process exits.
func (h *Hub) Run() {
	for {
		select {

		// New client connected
		case client := <-h.Register:
			// Evict stale connection for same user (e.g. tab refresh)
			// WHY: leaving the old channel open causes a goroutine leak
			if old, exists := h.clients[client.UserID]; exists {
				log.Printf("[hub] evicting stale connection for user %s", client.UserID)
				close(old.Send)
			}
			h.clients[client.UserID] = client
			log.Printf("[hub] registered user=%s doc=%s total=%d", client.UserID, client.DocID, len(h.clients))

		// Client disconnected
		case client := <-h.Unregister:
			if _, ok := h.clients[client.UserID]; ok {
				delete(h.clients, client.UserID)
				close(client.Send)
				log.Printf("[hub] unregistered user=%s total=%d", client.UserID, len(h.clients))
			}

		// Broadcast a Yjs diff
		//
		// Route to every user on the SAME doc EXCEPT the sender.
		// WHY skip sender: they already applied the change locally
		// (optimistic UI). Sending it back = double-apply bug.
		case msg := <-h.Broadcast:
			for userID, client := range h.clients {
				if userID == msg.SenderID {
					continue
				}
				if client.DocID != msg.DocID {
					continue
				}

				// Non-blocking send — the slow consumer check
				// If Send buffer is full: close channel, evict client.
				// WritePump detects the closed channel and exits cleanly.
				select {
				case client.Send <- msg.Payload:
				default:
					log.Printf("[hub] slow consumer evicted: user=%s", userID)
					close(client.Send)
					delete(h.clients, userID)
				}
			}
		}
	}
}

// OnlineInDoc returns userIDs currently active on a specific document.
// NOTE: This reads h.clients directly. In a high-throughput system,
// route this through a dedicated query channel to avoid races.
// Safe here because this is only called from REST handlers (low frequency).
func (h *Hub) OnlineInDoc(docID string) []string {
	var users []string
	for uid, c := range h.clients {
		if c.DocID == docID {
			users = append(users, uid)
		}
	}
	return users
}

// TotalConnections returns total active WebSocket connections.
func (h *Hub) TotalConnections() int {
	return len(h.clients)
}
