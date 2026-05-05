package analytics

/*

  tracker.go + flusher.go

  THE DEBOUNCING + BATCHING PATTERN

  PROBLEM: Users type ~3-5 chars/sec. With 10 users on
  one doc: 30-50 DB writes/sec just for analytics.
  At 100 concurrent docs: potentially 5,000 writes/sec.
  That kills your Postgres in minutes.

  SOLUTION:
  Tracker.Record() → just increments a number in RAM.
  Tracker.StartFlusher() → goroutine wakes every 30s,
    snapshots the map, resets it, batch-writes to DB.

  MEMORY MATH: 100 docs × 10 users × 3 int fields =
  3,000 integers ≈ 24KB of RAM. Negligible.

  CONCURRENCY: Record() acquires a mutex (contention is
  minimal — lock is held for ~100ns). Flusher acquires
  the same mutex, swaps the map (not zero-fills it),
  releases immediately, then writes to DB without the
  lock held — so Record() is never blocked by slow DB.

*/

import (
	"sync"
	"time"
)

// Activity holds the buffered stats for one (docID, userID) pair
// within a single flush window.
type Activity struct {
	EditCount  int
	BytesAdded int
	ActiveSecs int
}

// SaveFn is the DB write function injected at startup.
// Keeping it as a function type means you can swap in a mock for tests.
type SaveFn func(docID, userID string, a *Activity, windowEnd time.Time)

// Tracker buffers contribution data entirely in memory.
// Structure: buffer[docID][userID] = accumulated Activity
type Tracker struct {
	mu     sync.Mutex
	buffer map[string]map[string]*Activity
}

func NewTracker() *Tracker {
	return &Tracker{buffer: make(map[string]map[string]*Activity)}
}

// Record is called on EVERY WebSocket message from a user.
// It must be as fast as possible — it's in the hot path.
//
// LOCK ANALYSIS:
// Lock is held for ~100ns (map lookup + integer increment).
// Even at 10,000 messages/sec this is safe. If profiling
// shows contention here, switch to sync/atomic per-counter.
func (t *Tracker) Record(userID, docID string, bytes int) {
	t.mu.Lock()
	defer t.mu.Unlock()

	if t.buffer[docID] == nil {
		t.buffer[docID] = make(map[string]*Activity)
	}
	if t.buffer[docID][userID] == nil {
		t.buffer[docID][userID] = &Activity{}
	}

	a := t.buffer[docID][userID]
	a.EditCount++
	a.BytesAdded += bytes
}

// RecordActiveTime is called by the WebSocket heartbeat (every 30s)
// to record that a user has the document focused.
func (t *Tracker) RecordActiveTime(userID, docID string, secs int) {
	t.mu.Lock()
	defer t.mu.Unlock()

	if t.buffer[docID] == nil {
		t.buffer[docID] = make(map[string]*Activity)
	}
	if t.buffer[docID][userID] == nil {
		t.buffer[docID][userID] = &Activity{}
	}
	t.buffer[docID][userID].ActiveSecs += secs
}

// StartFlusher starts the background goroutine that writes to DB.
//
// KEY TRICK — map swap pattern:
// Instead of iterating and zeroing, we atomically replace the entire
// buffer with a fresh empty map. The old map (snapshot) is processed
// OUTSIDE the lock, so Record() is never blocked by DB latency.
func (t *Tracker) StartFlusher(interval time.Duration, saveFn SaveFn) {
	go func() {
		ticker := time.NewTicker(interval)
		defer ticker.Stop()

		for windowEnd := range ticker.C {
			// --- Critical section: swap the buffer ---
			t.mu.Lock()
			snapshot := t.buffer
			t.buffer = make(map[string]map[string]*Activity) // fresh map
			t.mu.Unlock()
			// ----------------------------------------
			// From here on: snapshot is ours alone. No lock needed.
			// Record() writes to the NEW t.buffer, not snapshot.

			for docID, users := range snapshot {
				for userID, activity := range users {
					if activity.EditCount == 0 {
						continue // nothing to write
					}
					// saveFn can be slow (DB write). That's OK — we're outside the lock.
					saveFn(docID, userID, activity, windowEnd)
				}
			}
		}
	}()
}

// GetSnapshot returns a copy of current (unflushed) activity.
// Used by the "live" analytics API to show real-time contribution bars.
func (t *Tracker) GetSnapshot(docID string) map[string]*Activity {
	t.mu.Lock()
	defer t.mu.Unlock()

	result := make(map[string]*Activity)
	if t.buffer[docID] == nil {
		return result
	}
	// Deep copy — we can't return a reference to the live map
	for uid, act := range t.buffer[docID] {
		copy := *act
		result[uid] = &copy
	}
	return result
}
