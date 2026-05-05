<div align="center">

```
███████╗ ██████╗ ██╗   ██╗ █████╗ ██╗     ██╗███╗   ██╗██╗  ██╗
██╔════╝██╔═══██╗██║   ██║██╔══██╗██║     ██║████╗  ██║██║ ██╔╝
█████╗  ██║   ██║██║   ██║███████║██║     ██║██╔██╗ ██║█████╔╝ 
██╔══╝  ██║▄▄ ██║██║   ██║██╔══██║██║     ██║██║╚██╗██║██╔═██╗ 
███████╗╚██████╔╝╚██████╔╝██║  ██║███████╗██║██║ ╚████║██║  ██╗
╚══════╝ ╚══▀▀═╝  ╚═════╝ ╚═╝  ╚═╝╚══════╝╚═╝╚═╝  ╚═══╝╚═╝  ╚═╝
```

**Offline-first collaborative document platform.**  
Real-time multi-user editing · CRDT sync · Contribution analytics · Task accountability

![Go](https://img.shields.io/badge/Go-1.22-00ADD8?style=flat-square&logo=go)
![Postgres](https://img.shields.io/badge/Postgres-16-4169E1?style=flat-square&logo=postgresql)
![Redis](https://img.shields.io/badge/Redis-7-DC382D?style=flat-square&logo=redis)
![WebSocket](https://img.shields.io/badge/WebSocket-CRDT-c8f542?style=flat-square)

</div>

---

## What is EqualInk?

EqualInk is a production-grade collaborative document platform where **every contributor's work is measured equally**. No invisible effort, no credit loss. Built with Go (backend) and TypeScript (frontend), using Yjs CRDTs so edits merge perfectly — even across offline users.

### Core features

| Feature | How it works |
|---|---|
| **Real-time co-editing** | WebSocket + Yjs CRDT binary diffs, conflict-free merging |
| **Offline-first** | Edits queue locally, replay on reconnect via state vector exchange |
| **OTP Auth** | Email or phone — 6-digit code, Redis TTL, JWT sessions |
| **Contribution analytics** | Per-user edit counts + bytes, buffered in memory, flushed every 30s |
| **Task assignment** | Assign tasks inside docs, permission-validated even for offline ops |
| **PDF export** | Full doc / contribution report / task summary — server-rendered |
| **Horizontal scaling** | Redis Pub/Sub bridges multiple server instances |

---

---

## Architecture overview

```
Browser (Yjs client)
      │
      │  Binary WebSocket frames (Yjs diffs)
      ▼
┌─────────────────────────────────────────────┐
│              Go HTTP Server                 │
│                                             │
│  ws/pumps.go                                │
│  ┌──────────────┐    ┌───────────────────┐  │
│  │  ReadPump    │───▶│  sync/handler.go  │  │
│  │  (goroutine) │    │  HandleUpdate()   │  │
│  └──────────────┘    └─────┬──────┬──────┘  │
│  ┌──────────────┐          │      │         │
│  │  WritePump   │◀─────────┘      │         │
│  │  (goroutine) │   hub.Broadcast │         │
│  └──────────────┘                 │         │
│                                   ▼         │
│  hub/hub.go          document/store.go      │
│  ┌───────────┐       ┌────────────────────┐ │
│  │ Hub.Run() │       │  AppendUpdate()    │ │
│  │ (single   │       │  (goroutine, async)│ │
│  │ goroutine)│       └────────────────────┘ │
│  └───────────┘                              │
│                       analytics/tracker.go  │
│                       ┌────────────────────┐│
│                       │  Record() O(1)     ││
│                       │  → flushed 30s     ││
│                       └────────────────────┘│
└─────────────────────────────────────────────┘
      │                          │
      │ Redis Pub/Sub            │ GORM
      ▼                          ▼
┌──────────┐              ┌──────────────┐
│  Redis   │              │  PostgreSQL  │
│  - OTP   │              │  - users     │
│  - PubSub│              │  - documents │
└──────────┘              │  - updates   │
                          │  - tasks     │
                          │  - analytics │
                          └──────────────┘
```

---

## Data flow: one keystroke to all collaborators

```
User A types "Hello"
     │
     │  Yjs encodes as binary diff (≈ 15 bytes)
     ▼
ReadPump receives frame
     │
     ▼
sync/handler.HandleUpdate(userID, docID, payload)
     │
     ├──▶ go store.AppendUpdate()    // async DB write, non-blocking
     │         └─▶ INSERT INTO updates (doc_id, user_id, payload)
     │
     ├──▶ hub.Broadcast ◀──────────────────────────────────────────┐
     │         └─▶ Hub.Run() routes to all users on same doc        │
     │               └─▶ WritePump sends to each browser            │
     │                                                               │
     ├──▶ tracker.Record(userID, docID, len(payload))  // O(1) RAM │
     │                                                               │
     └──▶ pubsub.Publish(docID, payload)                            │
               └─▶ Redis "doc:{id}" channel                         │
                     └─▶ Other server instances receive it ─────────┘
                           └─▶ their Hub.Broadcast reaches their local users
```

---

## Key engineering decisions

### Why channels instead of mutexes in Hub?
The Hub's `Run()` is the **only** goroutine that touches `clients map`. All other goroutines communicate via channels. This eliminates data races without a single `sync.Mutex`. At 10,000 concurrent users, there's zero lock contention.

### Why append-only updates, not update-in-place?
Every Yjs diff is `INSERT`ed, never `UPDATE`d. This gives a complete audit log and makes concurrent writes safe (no row contention). The background **Compactor** merges them into a single `Document.Blob` every 5 minutes, preventing unbounded table growth.

### Why buffer analytics in RAM instead of writing per-keystroke?
A user types ~3 chars/sec. With 10 users: 30 DB writes/sec just for analytics. With 100 docs: 3,000 writes/sec → DB thrash. The `Tracker` buffers in a `map[docID][userID]*Activity` and uses a **map-swap pattern** to flush every 30s: the lock is held for ~100ns (map swap), then DB writes happen outside the lock. Record() is never blocked by slow Postgres.

### Why Redis Pub/Sub for scaling?
Redis is already in the stack for OTP storage. Pub/Sub adds zero new infrastructure. One `PSubscribe("doc:*")` subscription covers all document channels. Latency is <1ms within a datacenter. For >100k users, upgrade path is NATS JetStream.

### Why OTP with `crypto/rand` not `math/rand`?
`math/rand` seeded with time is predictable — an attacker who knows the approximate seed can brute-force the 6-digit space in milliseconds. `crypto/rand` uses the OS entropy pool (CSPRNG), making OTPs cryptographically unpredictable.

---

## Getting started

### Prerequisites
- Go 1.22+
- Docker + Docker Compose (for local Postgres + Redis)

### 1. Clone and configure

```bash
git clone https://github.com/yourusername/equalink.git
cd equalink/equalink-backend

cp .env.example .env
# Edit .env:
#   DATABASE_URL=postgres://equalink:equalink@localhost:5432/equalink?sslmode=disable
#   JWT_SECRET=$(openssl rand -hex 32)
```

### 2. Start infrastructure

```bash
docker compose up -d
# Starts Postgres on :5432 and Redis on :6379
```

### 3. Run the backend

```bash
go mod tidy
go run main.go

# Output:
# EqualInk starting on :8080 (env: development)
# Background workers started: analytics flusher, document compactor
# EqualInk listening on :8080
```

### 4. Open the frontend

```bash
# Just open the file — no build step needed
open ../equalink-frontend/index.html

# Or serve it:
cd ../equalink-frontend && python3 -m http.server 3000
# Visit http://localhost:3000
```

### 5. Test auth
- Click **Try Demo** to skip OTP, or
- Enter any email → use OTP code `123456` (demo mode)

---

## API reference

### Auth (no JWT required)

| Method | Endpoint | Body | Response |
|---|---|---|---|
| POST | `/auth/send-otp` | `{ identifier }` | `{ session_token, method }` |
| POST | `/auth/verify-otp` | `{ session_token, code, name? }` | `{ jwt, user }` |

### WebSocket

```
GET /ws?token=<jwt>&doc=<docID>&sv=<base64-state-vector>
```

Sends/receives raw Yjs binary frames. On connect, server sends the current document blob.

### Documents (JWT required: `Authorization: Bearer <jwt>`)

| Method | Endpoint | Description |
|---|---|---|
| GET | `/api/docs` | List user's documents |
| POST | `/api/docs` | Create document `{ title }` |
| GET | `/api/docs/:id` | Get doc + online users |
| POST | `/api/docs/:id/tasks` | Assign task `{ title, assignee_id }` |
| GET | `/api/docs/:id/analytics` | Contribution stats |
| POST | `/api/docs/:id/export` | Export PDF `{ format }` |

---

## Environment variables

| Variable | Required | Description |
|---|---|---|
| `DATABASE_URL` | ✅ | Postgres connection string |
| `JWT_SECRET` | ✅ | 32-byte random hex for signing JWTs |
| `REDIS_URL` | ✅ | Redis host:port |
| `PORT` | — | Server port (default `:8080`) |
| `ENV` | — | `development` or `production` |
| `SMTP_HOST` | — | For email OTP delivery |
| `SMTP_USER` | — | SMTP username |
| `SMTP_PASS` | — | SMTP password / app password |
| `TWILIO_SID` | — | For SMS OTP (India: Fast2SMS) |
| `TWILIO_TOKEN` | — | Twilio auth token |

---

## Production deployment checklist

- [ ] Set `ENV=production` (disables GORM query logging)
- [ ] Set a strong random `JWT_SECRET` (32+ bytes)
- [ ] Restrict `CheckOrigin` in `ws/pumps.go` to your domain
- [ ] Set `Access-Control-Allow-Origin` to your frontend domain
- [ ] Use a managed Postgres (RDS / Supabase) and Redis (Elasticache / Upstash)
- [ ] Run behind a reverse proxy (nginx / Caddy) with TLS termination
- [ ] Set `DB.SetMaxOpenConns(25)` (already done in main.go)
- [ ] Add Redis distributed locking so only one instance runs the Compactor
- [ ] Wire up real SMTP (Resend.com recommended) and SMS (Twilio / Fast2SMS)
- [ ] Add `/health` endpoint for load balancer health checks

---

## Scaling path

```
Single server (this codebase)
    └─▶ Add Redis Pub/Sub (already implemented in pubsub/redis.go)
         └─▶ Run N instances behind a load balancer
              └─▶ For >100k users: replace Redis PubSub with NATS JetStream
                   └─▶ For document history/versioning: add S3 blob storage
```

---

## Tech stack

| Layer | Technology | Why |
|---|---|---|
| Language | Go 1.22 | Goroutines make 10k WebSocket connections trivial |
| WebSocket | gorilla/websocket | Battle-tested, fine-grained control over timeouts |
| ORM | GORM | AutoMigrate for dev speed; hooks for business logic |
| Database | PostgreSQL 16 | JSONB, UUID, upsert (ON CONFLICT DO UPDATE) |
| Cache / PubSub | Redis 7 | OTP TTL + cross-instance message bus |
| Auth | JWT (HS256) | Stateless — no session table, scales horizontally |
| CRDT | Yjs (client) | Conflict-free, offline-first, battle-tested in Notion/Linear |
| Frontend | HTML + CSS + Vanilla JS | Zero build step, instant open |

---

<div align="center">
Built with ♥ · <strong>EqualInk</strong> — where every contribution counts
</div>
