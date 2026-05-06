-- Run with: goose postgres "$DATABASE_URL" up
-- Or: psql $DATABASE_URL -f migrations/001_initial_schema.sql

-- +goose Up
-- +goose StatementBegin

CREATE EXTENSION IF NOT EXISTS "pgcrypto"; -- for gen_random_uuid()

CREATE TABLE IF NOT EXISTS users (
    id         UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    name       TEXT NOT NULL,
    email      TEXT UNIQUE DEFAULT '',
    phone      TEXT UNIQUE DEFAULT '',
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    deleted_at TIMESTAMPTZ
);

CREATE TABLE IF NOT EXISTS documents (
    id         UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    title      TEXT NOT NULL,
    blob       BYTEA,                         -- merged Yjs CRDT state
    created_by UUID NOT NULL REFERENCES users(id),
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    deleted_at TIMESTAMPTZ
);

CREATE TABLE IF NOT EXISTS document_members (
    doc_id  UUID NOT NULL REFERENCES documents(id) ON DELETE CASCADE,
    user_id UUID NOT NULL REFERENCES users(id)     ON DELETE CASCADE,
    role    TEXT NOT NULL DEFAULT 'editor',        -- owner | editor | viewer
    PRIMARY KEY (doc_id, user_id)
);

-- Append-only Yjs diff log
-- CompactedAt NULL = pending merge into documents.blob
CREATE TABLE IF NOT EXISTS updates (
    id           BIGSERIAL PRIMARY KEY,
    doc_id       UUID        NOT NULL REFERENCES documents(id) ON DELETE CASCADE,
    user_id      UUID        NOT NULL REFERENCES users(id),
    payload      BYTEA       NOT NULL,
    created_at   TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    compacted_at TIMESTAMPTZ             -- NULL until merged by compactor
);
CREATE INDEX IF NOT EXISTS idx_updates_pending ON updates (doc_id) WHERE compacted_at IS NULL;

-- Contribution analytics (written every 30s by analytics flusher)
CREATE TABLE IF NOT EXISTS contributions (
    id          BIGSERIAL PRIMARY KEY,
    doc_id      UUID        NOT NULL REFERENCES documents(id) ON DELETE CASCADE,
    user_id     UUID        NOT NULL REFERENCES users(id),
    edit_count  INT         NOT NULL DEFAULT 0,
    bytes_added INT         NOT NULL DEFAULT 0,
    active_secs INT         NOT NULL DEFAULT 0,
    window_end  TIMESTAMPTZ NOT NULL,
    UNIQUE (doc_id, user_id, window_end)
);

-- Tasks (soft-deleted for offline replay safety)
CREATE TABLE IF NOT EXISTS tasks (
    id          BIGSERIAL PRIMARY KEY,
    doc_id      UUID        NOT NULL REFERENCES documents(id) ON DELETE CASCADE,
    assignee_id UUID        REFERENCES users(id),
    created_by  UUID        NOT NULL REFERENCES users(id),
    title       TEXT        NOT NULL,
    status      TEXT        NOT NULL DEFAULT 'open',
    created_at  TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at  TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    deleted_at  TIMESTAMPTZ
);

-- +goose StatementEnd

-- +goose Down
DROP TABLE IF EXISTS tasks;
DROP TABLE IF EXISTS contributions;
DROP TABLE IF EXISTS updates;
DROP TABLE IF EXISTS document_members;
DROP TABLE IF EXISTS documents;
DROP TABLE IF EXISTS users;