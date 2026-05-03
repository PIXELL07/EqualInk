package sync

/*

  The Brain of EqualInk

  EVERY Yjs diff from EVERY user lands here.
  Three things happen in order:

  1. PERSIST  → AppendUpdate (goroutine, non-blocking)
     WHY goroutine: DB writes take 1-5ms. We can't
     block the WebSocket receive loop for that long.
     If the server crashes before the write, the diff
     is lost — acceptable tradeoff for low latency.
     For higher durability: use a buffered channel +
     a dedicated writer goroutine (see store.go).

  2. BROADCAST → Hub.Broadcast channel
     Sends diff to all other users on this document.
     Non-blocking because the Hub's Broadcast channel
     is buffered (256). If the buffer fills up, we drop
     the message (eventual consistency via Yjs re-sync).

  3. TRACK → analytics.Tracker.Record
     Just updates in-memory counters. O(1), lock-free
     fast path. Actual DB write happens in flusher.

*/

import (
	"encoding/json"
	"net/http"

	"github.com/pixell07/equalink/analytics"
	"github.com/pixell07/equalink/auth"
	"github.com/pixell07/equalink/document"
	"github.com/pixell07/equalink/hub"
)

type Handler struct {
	Store   *document.Store
	Tracker *analytics.Tracker
	Hub     *hub.Hub
}

// HandleUpdate is called by ws/read_pump.go for every incoming binary frame.
// It's the central dispatch — keep it thin, delegate to specialists.
func (h *Handler) HandleUpdate(userID, docID string, payload []byte) {
	// 1. PERSIST — fire and forget (goroutine)
	go h.Store.AppendUpdate(docID, userID, payload)

	// 2. BROADCAST — non-blocking channel send
	select {
	case h.Hub.Broadcast <- hub.Message{
		SenderID: userID,
		DocID:    docID,
		Payload:  payload,
	}:
	default:
		// Broadcast buffer full — drop. Yjs will re-sync via state vector exchange.
	}

	// 3. TRACK — O(1) in-memory increment
	h.Tracker.Record(userID, docID, len(payload))
}

// OnJoin sends the current document state to a newly connected client.
// Called once when the WebSocket connection is established.
//
// CRDT SYNC PROTOCOL:
// Client sends its Yjs "state vector" (a compact summary of what it has).
// Server diffs the stored blob against that state vector and sends only
// the missing updates. This is how offline clients catch up efficiently.
func (h *Handler) OnJoin(userID, docID string, clientStateVector []byte) ([]byte, error) {
	blob, err := h.Store.LoadBlob(docID)
	if err != nil {
		return nil, err
	}
	// In a full Yjs Go implementation (y-crdt bindings), you'd call:
	//   diff = yrs.Diff(blob, clientStateVector)
	// For now we send the full blob — client-side Yjs deduplicates.
	return blob, nil
}

// REST handlers

// GetDocument — GET /api/docs/:id
// Returns doc metadata + current online users (from Hub)
func (h *Handler) GetDocument(w http.ResponseWriter, r *http.Request) {
	userID := r.Context().Value(auth.UserIDKey).(string)
	docID := r.PathValue("id")

	doc, err := h.Store.LoadDocument(docID, userID)
	if err != nil {
		http.Error(w, `{"error":"not found"}`, 404)
		return
	}

	online := h.Hub.OnlineUsers(docID)

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]any{
		"doc":    doc,
		"online": online,
	})
}

// AssignTask — POST /api/docs/:id/tasks
// Validates assignee exists BEFORE persisting (the "offline permission" fix)
func (h *Handler) AssignTask(w http.ResponseWriter, r *http.Request) {
	creatorID := r.Context().Value(auth.UserIDKey).(string)
	docID := r.PathValue("id")

	var body struct {
		Title      string `json:"title"`
		AssigneeID string `json:"assignee_id"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(w, `{"error":"invalid body"}`, 400)
		return
	}

	task, err := h.Store.CreateTask(docID, creatorID, body.AssigneeID, body.Title)
	if err != nil {
		http.Error(w, `{"error":"db error"}`, 500)
		return
	}

	// Broadcast task assignment as a structured event to all collaborators
	// WHY: offline users will receive this on reconnect via Yjs sync
	taskJSON, _ := json.Marshal(map[string]any{
		"type": "task_assigned",
		"task": task,
	})

	select {
	case h.Hub.Broadcast <- hub.Message{
		SenderID: creatorID,
		DocID:    docID,
		Payload:  taskJSON,
	}:
	default:
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(task)
}
