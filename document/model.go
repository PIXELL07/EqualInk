package document

/*

  — GORM Structs + Hooks               
                                                          
  HOW IT WORKS:                                           
  These are the DB tables. GORM reads the struct tags    
  and auto-creates/migrates the schema.                  
                                                          
  KEY DESIGN DECISIONS:                                  
  - Document stores a Blob (merged Yjs state vector).    
    NOT the full text — just the CRDT binary.            
  - Update stores individual diffs (append-only log).   
    Compactor merges them into Blob periodically.        
  - Contribution is the analytics record, flushed       
    from memory every 30s by the analytics flusher.     
  - Task has a soft-delete (DeletedAt) so offline        
    clients can replay deletions when they reconnect.   

*/

import (
	"time"
	"gorm.io/gorm"
)

// User 

type User struct {
	ID        string         `gorm:"primaryKey;type:uuid;default:gen_random_uuid()" json:"id"`
	Name      string         `gorm:"not null" json:"name"`
	Email     string         `gorm:"uniqueIndex;default:''" json:"email"`
	Phone     string         `gorm:"uniqueIndex;default:''" json:"phone"`
	CreatedAt time.Time      `json:"created_at"`
	UpdatedAt time.Time      `json:"updated_at"`
	DeletedAt gorm.DeletedAt `gorm:"index" json:"-"`

	// Associations — loaded only when needed (not eager)
	Docs  []DocumentMember `gorm:"foreignKey:UserID" json:"-"`
	Tasks []Task           `gorm:"foreignKey:AssigneeID" json:"-"`
}

// Document
//
// WHY Blob []byte?
// Yjs produces a binary state vector that encodes the ENTIRE document
// history. We merge all Updates into this Blob periodically.
// On reconnect, a client sends its state vector, and we diff the
// Blob against it to send only the missing updates. Efficient.

type Document struct {
	ID        string         `gorm:"primaryKey;type:uuid;default:gen_random_uuid()" json:"id"`
	Title     string         `gorm:"not null" json:"title"`
	Blob      []byte         `gorm:"type:bytea" json:"-"` // merged Yjs state — never send raw to client
	CreatedBy string         `gorm:"type:uuid;not null" json:"created_by"`
	CreatedAt time.Time      `json:"created_at"`
	UpdatedAt time.Time      `json:"updated_at"`
	DeletedAt gorm.DeletedAt `gorm:"index" json:"-"`

	Members []DocumentMember `gorm:"foreignKey:DocID" json:"members,omitempty"`
}

// DocumentMember is the join table: who has access to which doc
type DocumentMember struct {
	DocID  string `gorm:"primaryKey;type:uuid"`
	UserID string `gorm:"primaryKey;type:uuid"`
	Role   string `gorm:"default:'editor'"` // "owner" | "editor" | "viewer"

	User document User `gorm:"foreignKey:UserID" json:"user,omitempty"`
}

// Update (append-only diff log)
//
// WHY append-only?
// Every keystroke in Yjs produces a tiny binary diff. We never
// UPDATE these rows — we only INSERT. This is the "write-ahead log"
// of the document. The compactor later merges them into Document.Blob.
//
// PRODUCTION CONCERN: this table can grow very large.
// → CompactedAt == nil means "not yet merged into blob"
// → After compaction, we set CompactedAt = now (don't delete — audit trail)

type Update struct {
	ID          uint      `gorm:"primaryKey;autoIncrement"`
	DocID       string    `gorm:"type:uuid;index:idx_doc_uncompacted"`
	UserID      string    `gorm:"type:uuid"`
	Payload     []byte    `gorm:"type:bytea;not null"` // raw Yjs binary diff
	CreatedAt   time.Time `gorm:"index"`
	CompactedAt *time.Time `gorm:"index:idx_doc_uncompacted"` // nil = pending compaction
}

// Contribution (analytics)
//
// WHY NOT write per-keystroke?
// A user types 200 chars/min. That's 200 DB writes/min per user.
// With 10 concurrent users: 2,000 writes/min = DB thrash.
//
// INSTEAD: analytics.Tracker buffers in memory (map[docID][userID])
// and this record is written ONCE every 30 seconds per user per doc.
// The numbers are cumulative for that flush window.

type Contribution struct {
	ID         uint      `gorm:"primaryKey;autoIncrement"`
	DocID      string    `gorm:"type:uuid;index"`
	UserID     string    `gorm:"type:uuid;index"`
	EditCount  int       `gorm:"default:0"`
	BytesAdded int       `gorm:"default:0"`
	ActiveSecs int       `gorm:"default:0"` // seconds the user had the doc focused
	WindowEnd  time.Time `gorm:"index"`     // when this 30s window closed
}

// Task 
//
// Tasks are assigned inside documents. Offline-first challenge:
// if User A assigns to User B while offline, and User B was deleted
// by an admin, we need to handle this gracefully on reconnect.
//
// HOW: BeforeCreate hook validates AssigneeID still exists.
//      If not, task is marked Status = "unassigned" instead of erroring.

type Task struct {
	ID         uint           `gorm:"primaryKey;autoIncrement" json:"id"`
	DocID      string         `gorm:"type:uuid;index" json:"doc_id"`
	AssigneeID string         `gorm:"type:uuid" json:"assignee_id"`
	CreatedBy  string         `gorm:"type:uuid" json:"created_by"`
	Title      string         `gorm:"not null" json:"title"`
	Status     string         `gorm:"default:'open'"` // "open" | "done" | "unassigned"
	CreatedAt  time.Time      `json:"created_at"`
	UpdatedAt  time.Time      `json:"updated_at"`
	DeletedAt  gorm.DeletedAt `gorm:"index" json:"-"` // soft-delete for offline replay

	Assignee *User `gorm:"foreignKey:AssigneeID" json:"assignee,omitempty"`
}

// GORM Hooks 
//
// Hooks run automatically before DB operations.
// BeforeCreate on Task = our "permission check" that runs even
// for offline operations replayed on reconnect.

func (t *Task) BeforeCreate(tx *gorm.DB) error {
	// Validate assignee exists in this document
	// If they don't, gracefully downgrade to unassigned
	var count int64
	tx.Model(&DocumentMember{}).
		Where("doc_id = ? AND user_id = ?", t.DocID, t.AssigneeID).
		Count(&count)

	if count == 0 && t.AssigneeID != "" {
		t.AssigneeID = ""
		t.Status = "unassigned"
		// Don't error — degrade gracefully so offline op still persists
	}
	return nil
}

// BeforeUpdate on Document = auto-timestamp for activity tracking
func (d *Document) BeforeUpdate(tx *gorm.DB) error {
	d.UpdatedAt = time.Now()
	return nil
}
