package document

// Store handles all postgres operations for documents.
type Store struct{}

// LoadBlob returns the latest merged Yjs state for a doc.
func (s *Store) LoadBlob(docID string) ([]byte, error) { return nil, nil }

// AppendUpdate inserts one Yjs incremental diff into the update log.
func (s *Store) AppendUpdate(docID, userID string, payload []byte) error { return nil }

// SnapshotState merges all pending updates into the blob column.
func (s *Store) SnapshotState(docID string) error { return nil }
