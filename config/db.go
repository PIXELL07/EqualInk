package config

/*

  — Postgres Pool + GORM Setup

  HOW IT WORKS:
  Opens a single *gorm.DB instance shared by the whole
  app. GORM maintains a connection pool internally.

  POOL SETTINGS (why these numbers):
  MaxOpenConns 25  → max simultaneous DB connections.
    Postgres default max_connections = 100.
    With 4 server instances × 25 = 100. Perfect fit.
  MaxIdleConns 10  → keep 10 warm connections ready.
    Cold connection = 5-50ms TLS handshake overhead.
  ConnMaxLifetime 5min → recycle stale connections.
    Without this, connections behind a load balancer
    or NAT can go stale silently and cause 1-request
    errors that are hard to debug.

  AutoMigrate: creates/alters tables to match structs.
  For production use goose or atlas for versioned SQL.

*/

import (
	"log"
	"time"

	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"

	"github.com/pixell07/equalink/document"
)

// NewDB opens the Postgres pool and runs auto-migrations.
// Returns *gorm.DB — pass this everywhere, never open a second connection.
func NewDB(cfg *Config) *gorm.DB {
	// Silence query logs in production (saves gigabytes of log volume)
	// Show them in development for debugging
	gormLogger := logger.Default.LogMode(logger.Silent)
	if !cfg.IsProduction() {
		gormLogger = logger.Default.LogMode(logger.Info)
	}

	db, err := gorm.Open(postgres.Open(cfg.DatabaseURL), &gorm.Config{
		Logger:                                   gormLogger,
		DisableForeignKeyConstraintWhenMigrating: false,
		// PrepareStmt caches prepared statements — faster repeated queries
		PrepareStmt: true,
	})
	if err != nil {
		log.Fatalf("[db] failed to connect to postgres: %v\nURL: %s", err, cfg.DatabaseURL)
	}

	// Get the underlying *sql.DB to configure the pool
	sqlDB, err := db.DB()
	if err != nil {
		log.Fatalf("[db] failed to get sql.DB: %v", err)
	}

	sqlDB.SetMaxOpenConns(cfg.DBMaxOpen)
	sqlDB.SetMaxIdleConns(cfg.DBMaxIdle)
	sqlDB.SetConnMaxLifetime(5 * time.Minute)
	sqlDB.SetConnMaxIdleTime(2 * time.Minute)

	// Ping to verify connection before proceeding
	if err := sqlDB.Ping(); err != nil {
		log.Fatalf("[db] postgres ping failed: %v", err)
	}
	log.Println("[db] Postgres connected")

	// AutoMigrate: adds missing columns/tables, never drops existing ones
	// Safe to run on every startup
	err = db.AutoMigrate(
		&document.User{},
		&document.Document{},
		&document.DocumentMember{},
		&document.Update{},
		&document.Contribution{},
		&document.Task{},
	)
	if err != nil {
		log.Fatalf("[db] AutoMigrate failed: %v", err)
	}
	log.Println("[db] Migrations applied")

	return db
}
