package document

import "time"

// StartCompactor runs a background ticker that merges the update
// log into a single blob every interval, preventing unbounded growth.
func StartCompactor(store *Store, interval time.Duration) {
	go func() {
		ticker := time.NewTicker(interval)
		for range ticker.C {
			// TODO: list all dirty docIDs, call store.SnapshotState for each
		}
	}()
}
