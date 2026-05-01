package document

import (
	"time"

	"gorm.io/gorm"
)

type Document struct {
	gorm.Model
	ID    string `gorm:"primaryKey"`
	Title string
	Blob  []byte // current Yjs state vector (merged snapshot)
}

type Update struct {
	gorm.Model
	DocID     string
	UserID    string
	Payload   []byte
	CreatedAt time.Time
}

type User struct {
	gorm.Model
	ID    string `gorm:"primaryKey"`
	Name  string
	Email string
}

type Task struct {
	gorm.Model
	DocID      string
	AssigneeID string
	Title      string
	Done       bool
}
