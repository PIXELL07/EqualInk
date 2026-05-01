package sync

import (
	"github.com/pixell07/equalink/analytics"
	"github.com/pixell07/equalink/document"
	"github.com/pixell07/equalink/hub"
)

// Handler is the brain — receives a diff and does 3 things.
type Handler struct {
	Store   *document.Store
	Tracker *analytics.Tracker
}

// HandleUpdate: persist → broadcast → track.
func (h *Handler) HandleUpdate(userID, docID string, payload []byte, hub *hub.Hub) {
	go h.Store.AppendUpdate(docID, userID, payload)

	hub.Broadcast <- hub.Message{
		SenderID: userID,
		DocID:    docID,
		Payload:  payload,
	}

	h.Tracker.Record(userID, docID, len(payload))
}
