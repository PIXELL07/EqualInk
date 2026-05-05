package document

/*

  — Postgres Operations

  THREE KEY FUNCTIONS:
  1. AppendUpdate  → INSERT one Yjs diff (hot path)
  2. LoadBlob      → SELECT merged state for new joiner
  3. SnapshotState → MERGE update log into blob
    (called by compactor background job)

  THE APPEND-ONLY PATTERN:
  We never UPDATE the update log — only INSERT.
  This gives us a complete audit trail and makes
  concurrent writes safe (no row contention).
  The downside: the updates table grows forever.
  The compactor solves this by merging + marking done.

*/

import (
	"fmt"
	"time"

	"gorm.io/gorm"
)

type Store struct {
	db *gorm.DB
}

func NewStore(db *gorm.DB) *Store {
	return &Store{db: db}
}

// AppendUpdate inserts one Yjs binary diff into the update log.
// Called asynchronously (go AppendUpdate(...)) from sync/handler.go.
//
// WHY NOT batch inserts here?
// Each update is independent and small. The goroutine fire-and-forget
// pattern means we never block the WebSocket loop. If DB is slow,
// updates queue up in goroutine pool — Go handles this gracefully.
// For extremely high throughput: use a channel + dedicated writer
// that collects 100ms worth of updates and bulk inserts them.
func (s *Store) AppendUpdate(docID, userID string, payload []byte) error {
	return s.db.Create(&Update{
		DocID:     docID,
		UserID:    userID,
		Payload:   payload,
		CreatedAt: time.Now(),
	}).Error
}

// LoadBlob returns the current merged Yjs state for a document.
// New joining clients receive this to initialize their local state.
//
// WHY return just the Blob, not the full update log?
// The Blob is the Yjs "encoded state vector" — a compact binary that
// represents all confirmed edits. Sending 50KB of blob is far cheaper
// than sending 10,000 individual 50-byte updates.
func (s *Store) LoadBlob(docID string) ([]byte, error) {
	var doc Document
	err := s.db.Select("blob").Where("id = ?", docID).First(&doc).Error
	if err != nil {
		return nil, err
	}
	return doc.Blob, nil
}

// LoadDocument fetches doc metadata + members (with access check).
func (s *Store) LoadDocument(docID, userID string) (*Document, error) {
	var doc Document
	err := s.db.
		Preload("Members.User").
		Where("id = ? AND EXISTS (SELECT 1 FROM document_members WHERE doc_id = ? AND user_id = ?)", docID, docID, userID).
		First(&doc).Error
	return &doc, err
}

// SnapshotState merges all pending Updates into Document.Blob.
// Called by StartCompactor every N minutes.
//
// MERGE LOGIC (with real y-crdt Go bindings):
//
//	doc := yrs.NewDoc()
//	yrs.ApplyUpdate(doc, existingBlob)   // load current state
//	for each pending update:
//	  yrs.ApplyUpdate(doc, update.Payload)  // apply each diff
//	newBlob = yrs.EncodeStateAsUpdate(doc)  // encode merged state
//
// For now we concatenate — a real Yjs port would do proper merge.
func (s *Store) SnapshotState(docID string) error {
	return s.db.Transaction(func(tx *gorm.DB) error {
		// Load existing blob
		var doc Document
		if err := tx.Select("id, blob").Where("id = ?", docID).First(&doc).Error; err != nil {
			return err
		}

		// Load all uncompacted updates for this doc
		var updates []Update
		if err := tx.Where("doc_id = ? AND compacted_at IS NULL", docID).
			Order("created_at ASC").
			Find(&updates).Error; err != nil {
			return err
		}

		if len(updates) == 0 {
			return nil // nothing to compact
		}

		// Build new blob: existing + all new updates
		// In production: use y-crdt bindings for proper CRDT merge
		newBlob := doc.Blob
		for _, u := range updates {
			newBlob = append(newBlob, u.Payload...)
		}

		// Update the blob
		now := time.Now()
		if err := tx.Model(&doc).Update("blob", newBlob).Error; err != nil {
			return err
		}

		// Mark updates as compacted (don't delete — audit trail)
		ids := make([]uint, len(updates))
		for i, u := range updates {
			ids[i] = u.ID
		}
		return tx.Model(&Update{}).Where("id IN ?", ids).Update("compacted_at", now).Error
	})
}

// CreateTask creates a task with the GORM BeforeCreate hook running automatically.
// The hook validates the assignee — if they don't have access, task becomes unassigned.
func (s *Store) CreateTask(docID, creatorID, assigneeID, title string) (*Task, error) {
	task := &Task{
		DocID:      docID,
		AssigneeID: assigneeID,
		CreatedBy:  creatorID,
		Title:      title,
		Status:     "open",
	}
	if err := s.db.Create(task).Error; err != nil {
		return nil, err
	}
	// Reload with assignee info
	s.db.Preload("Assignee").First(task, task.ID)
	return task, nil
}

// SaveContribution upserts a contribution record for the analytics flusher.
// Uses INSERT ... ON CONFLICT DO UPDATE (Postgres upsert) to accumulate
// multiple flush windows into daily totals.
func (s *Store) SaveContribution(docID, userID string, a interface{}, windowEnd time.Time) error {
	// Cast a to *analytics.Activity in production
	// Keeping interface{} here to avoid import cycle for this file
	return s.db.Exec(`
		INSERT INTO contributions (doc_id, user_id, edit_count, bytes_added, window_end)
		VALUES (?, ?, ?, ?, ?)
		ON CONFLICT (doc_id, user_id, window_end)
		DO UPDATE SET
			edit_count  = contributions.edit_count  + EXCLUDED.edit_count,
			bytes_added = contributions.bytes_added + EXCLUDED.bytes_added
	`, docID, userID, 0, 0, windowEnd).Error
}

// Compactor

/*
StartCompactor runs as a background goroutine.

HOW IT WORKS:
Every `interval`, it finds all documents that have uncompacted updates
and calls SnapshotState on each one.

WHY NEEDED:
The updates table grows by 1 row per WebSocket message.
A team editing for 1 hour at 5 users × 2 edits/sec = 36,000 rows.
After a week: ~5 million rows. Queries slow down. Disk fills up.

The compactor merges those rows into the Document.Blob column,
marks them compacted, and the next time anyone opens the doc,
they get a single compact blob instead of replaying 36,000 updates.

PRODUCTION CONCERN: run ONE compactor per deployment.
With Redis distributed locking (SETNX), multiple instances can
coordinate so only one runs the compactor at a time.
*/
func StartCompactor(store *Store, interval time.Duration) {
	go func() {
		ticker := time.NewTicker(interval)
		defer ticker.Stop()

		for range ticker.C {
			// Find all docIDs with uncompacted updates
			var docIDs []string
			store.db.
				Model(&Update{}).
				Where("compacted_at IS NULL").
				Distinct("doc_id").
				Pluck("doc_id", &docIDs)

			for _, docID := range docIDs {
				if err := store.SnapshotState(docID); err != nil {
					fmt.Printf("[compactor] error compacting %s: %v\n", docID, err)
				}
			}
		}
	}()
}
