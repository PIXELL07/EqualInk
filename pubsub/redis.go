package pubsub

/*

  — Horizontal Scaling Bridge

  THE PROBLEM THIS SOLVES:
  With 1 server: Hub.Broadcast reaches all users.
  With 2+ servers: User A is on Server 1, User B is on
  Server 2. Hub on Server 1 has no knowledge of B.
  A's edit never reaches B.

  THE SOLUTION — Redis Pub/Sub:
  When Server 1 receives a diff from User A:
  1. Hub.Broadcast → sends to A's co-editors on Server 1
  2. Redis.Publish("doc:{docID}", payload)
     → Redis fans out to ALL subscribed servers
  3. Server 2's Subscribe goroutine receives it
     → feeds it into Server 2's Hub.Broadcast
     → Server 2's Hub sends it to User B

  WHY Redis Pub/Sub and not Kafka/NATS?
  - Redis is already in the stack (OTP storage)
  - Pub/Sub is fire-and-forget (no persistence needed)
  - Latency < 1ms within same datacenter
  - For > 100k concurrent users: upgrade to NATS JetStream

*/

import (
	"context"
	"encoding/json"
	"fmt"
	"log"

	appHub "github.com/pixell07/equalink/hub"
	"github.com/redis/go-redis/v9"
)

// RedisAdapter is the pub/sub bridge between server instances.
type RedisAdapter struct {
	rdb *redis.Client
}

func NewRedisAdapter(rdb *redis.Client) *RedisAdapter {
	return &RedisAdapter{rdb: rdb}
}

// crossInstanceMessage is what we serialize into Redis.
// We can't send raw bytes directly because we need to know
// which doc to route to when received on the other side.
type crossInstanceMessage struct {
	SenderID string `json:"sender_id"`
	DocID    string `json:"doc_id"`
	Payload  []byte `json:"payload"` // raw Yjs binary, base64-encoded by json.Marshal
}

// Publish sends a diff to Redis so other server instances can relay it.
// Called from sync/handler.go AFTER local Hub.Broadcast.
//
// WHY after, not instead of?
// Local Hub.Broadcast is instant (in-memory channel).
// Redis round-trip adds ~0.5ms. Users on the SAME server
// get the update immediately; users on other servers get it
// after the Redis round-trip. This is the correct priority.
func (r *RedisAdapter) Publish(docID, senderID string, payload []byte) error {
	msg := crossInstanceMessage{
		SenderID: senderID,
		DocID:    docID,
		Payload:  payload,
	}
	data, err := json.Marshal(msg)
	if err != nil {
		return err
	}
	// Channel name = "doc:{docID}" — one channel per document
	// WHY per-doc channels? Allows targeted subscriptions.
	// A server only subscribes to docs that have active local users.
	channel := fmt.Sprintf("doc:%s", docID)
	return r.rdb.Publish(context.Background(), channel, data).Err()
}

// Subscribe starts a long-running goroutine that listens for
// cross-instance messages and feeds them into the local Hub.
//
// PATTERN: psubscribe "doc:*" — subscribe to ALL doc channels at once.
// WHY pattern subscribe? We don't know which docs will be active.
// The Redis broker efficiently routes only relevant messages.
func (r *RedisAdapter) Subscribe(h *appHub.Hub) {
	ctx := context.Background()
	pubsub := r.rdb.PSubscribe(ctx, "doc:*")

	go func() {
		defer pubsub.Close()
		ch := pubsub.Channel()

		log.Println("[pubsub] Subscribed to Redis doc:* channels")

		for redisMsg := range ch {
			var msg crossInstanceMessage
			if err := json.Unmarshal([]byte(redisMsg.Payload), &msg); err != nil {
				log.Printf("[pubsub] bad message: %v", err)
				continue
			}

			// Feed into local Hub — same as if the message came from a local WebSocket
			// The Hub will route it to any locally-connected users on this doc.
			// If no local users are on this doc, the message is silently dropped.
			select {
			case h.Broadcast <- appHub.Message{
				SenderID: msg.SenderID,
				DocID:    msg.DocID,
				Payload:  msg.Payload,
			}:
			default:
				// Broadcast buffer full — acceptable drop
				// Yjs guarantees eventual consistency via state vector exchange
			}
		}
	}()
}

// MockAdapter is used in tests — no Redis required
type MockAdapter struct {
	Published []crossInstanceMessage
}

func (m *MockAdapter) Publish(docID, senderID string, payload []byte) error {
	m.Published = append(m.Published, crossInstanceMessage{
		SenderID: senderID, DocID: docID, Payload: payload,
	})
	return nil
}
func (m *MockAdapter) Subscribe(h *appHub.Hub) {} // no-op
