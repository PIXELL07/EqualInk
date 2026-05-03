package sync

/*

 auth/handler.go — OTP Authentication (Email + Phone)

  HOW IT WORKS:
  1. Client POSTs /auth/send-otp  { identifier }
     → We detect email vs phone
     → Generate a 6-digit code, store in Redis (5 min)
     → Fire a goroutine to send it via SMTP/SMS
     → Return { session_token } (unsigned, for tracking)

  2. Client POSTs /auth/verify-otp { session, code }
     → Load OTP from Redis, compare
     → On match: create/find User in DB
     → Sign a JWT (24h), delete OTP from Redis
     → Return { jwt, user }

  WHY REDIS for OTP storage?
  OTPs are ephemeral (5 min TTL). Storing in Postgres
  wastes a row write + forces a DELETE on verify.
  Redis SET with EX = automatic expiry, zero cleanup.

*/

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"fmt"
	"math/big"
	"net/http"
	"strings"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/redis/go-redis/v9"
	"github.com/yourusername/equalink/config"
	"github.com/yourusername/equalink/document"
	"gorm.io/gorm"
)

type Handler struct {
	db    *gorm.DB
	redis *redis.Client
	cfg   *config.Config
}

func NewHandler(db *gorm.DB, rdb *redis.Client, cfg *config.Config) *Handler {
	return &Handler{db: db, redis: rdb, cfg: cfg}
}

// Request / Response types

type SendOTPRequest struct {
	Identifier string `json:"identifier"` // email or E.164 phone
}

type SendOTPResponse struct {
	SessionToken string `json:"session_token"` // opaque, used in verify step
	Method       string `json:"method"`        // "email" or "sms"
}

type VerifyOTPRequest struct {
	SessionToken string `json:"session_token"`
	Code         string `json:"code"`
	Name         string `json:"name,omitempty"` // only on first signup
}

type VerifyOTPResponse struct {
	JWT  string        `json:"jwt"`
	User document.User `json:"user"`
}

// SendOTP — POST /auth/send-otp
//
// LOGIC: detect channel → generate code → Redis store → send async
func (h *Handler) SendOTP(w http.ResponseWriter, r *http.Request) {
	var req SendOTPRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		httpError(w, "invalid body", 400)
		return
	}
	req.Identifier = strings.TrimSpace(strings.ToLower(req.Identifier))
	if req.Identifier == "" {
		httpError(w, "identifier required", 400)
		return
	}

	// Detect email vs phone
	// WHY: same endpoint handles both, channel dictates delivery
	method := "email"
	if strings.HasPrefix(req.Identifier, "+") {
		method = "sms"
	}

	// Generate 6-digit OTP using crypto/rand (NOT math/rand — security matters)
	// WHY crypto/rand: math/rand is predictable if seeded with time
	code, err := generateOTP(6)
	if err != nil {
		httpError(w, "could not generate OTP", 500)
		return
	}

	// Session token = random 32-byte hex (used as Redis key prefix)
	sessionToken, err := randomHex(16)
	if err != nil {
		httpError(w, "server error", 500)
		return
	}

	// Store: Redis key = "otp:{session}:{identifier}" → value = code, TTL = 5min
	// WHY namespaced key: prevents collision if same user opens two tabs
	ctx := r.Context()
	redisKey := fmt.Sprintf("otp:%s:%s", sessionToken, req.Identifier)
	if err := h.redis.Set(ctx, redisKey, code, 5*time.Minute).Err(); err != nil {
		httpError(w, "redis error", 500)
		return
	}

	// Send OTP asynchronously — don't block the HTTP response
	// WHY goroutine: SMTP/SMS can take 200-800ms; client doesn't need to wait
	go func() {
		if method == "email" {
			sendEmailOTP(req.Identifier, code, h.cfg)
		} else {
			sendSMSOTP(req.Identifier, code, h.cfg)
		}
	}()

	writeJSON(w, SendOTPResponse{SessionToken: sessionToken, Method: method})
}

// VerifyOTP — POST /auth/verify-otp
//
// LOGIC: load OTP → compare → upsert user → sign JWT → delete OTP
func (h *Handler) VerifyOTP(w http.ResponseWriter, r *http.Request) {
	var req VerifyOTPRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		httpError(w, "invalid body", 400)
		return
	}

	// We need the identifier to reconstruct the Redis key.
	// Client sends it back (it's not secret, the code IS the secret).
	// For brevity here we expect identifier in a header or the body.
	identifier := r.Header.Get("X-Identifier")
	if identifier == "" {
		httpError(w, "X-Identifier header required", 400)
		return
	}

	ctx := r.Context()
	redisKey := fmt.Sprintf("otp:%s:%s", req.SessionToken, identifier)

	// Load stored code from Redis
	storedCode, err := h.redis.Get(ctx, redisKey).Result()
	if err == redis.Nil {
		// Key expired or never existed
		httpError(w, "OTP expired or invalid", 401)
		return
	}
	if err != nil {
		httpError(w, "redis error", 500)
		return
	}

	// Constant-time compare to prevent timing attacks
	// WHY: naive == comparison leaks info about how many chars matched
	if !secureEqual(req.Code, storedCode) {
		httpError(w, "incorrect OTP", 401)
		return
	}

	// OTP is correct — delete immediately (one-time use)
	h.redis.Del(ctx, redisKey)

	// Upsert user — find by identifier or create new
	// WHY FirstOrCreate: handles sign-up and sign-in in one path
	user := document.User{Email: identifier}
	if strings.HasPrefix(identifier, "+") {
		user = document.User{Phone: identifier}
	}
	if req.Name != "" {
		user.Name = req.Name
	}

	result := h.db.Where(document.User{Email: identifier}).
		Attrs(document.User{Name: req.Name}).
		FirstOrCreate(&user)
	if result.Error != nil {
		httpError(w, "db error", 500)
		return
	}

	// Sign JWT — 24h expiry, signed with HMAC-256
	// WHY JWT: stateless auth; Go WebSocket handler validates it on upgrade
	token := jwt.NewWithClaims(jwt.SigningMethodHS256, jwt.MapClaims{
		"sub":  user.ID,
		"name": user.Name,
		"exp":  time.Now().Add(24 * time.Hour).Unix(),
	})
	signed, err := token.SignedString([]byte(h.cfg.JWTSecret))
	if err != nil {
		httpError(w, "could not sign token", 500)
		return
	}

	writeJSON(w, VerifyOTPResponse{JWT: signed, User: user})
}

// Helpers

func generateOTP(digits int) (string, error) {
	max := new(big.Int)
	max.Exp(big.NewInt(10), big.NewInt(int64(digits)), nil)
	n, err := rand.Int(rand.Reader, max)
	if err != nil {
		return "", err
	}
	// Zero-pad to ensure fixed length (e.g. 000042 not 42)
	return fmt.Sprintf("%0*d", digits, n), nil
}

func randomHex(n int) (string, error) {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return fmt.Sprintf("%x", b), nil
}

// secureEqual avoids short-circuit evaluation
func secureEqual(a, b string) bool {
	if len(a) != len(b) {
		return false
	}
	var diff byte
	for i := range a {
		diff |= a[i] ^ b[i]
	}
	return diff == 0
}

func sendEmailOTP(to, code string, cfg *config.Config) {
	// Use cfg.SMTPHost, cfg.SMTPUser, cfg.SMTPPass
	// Integrate net/smtp or a service like Resend / SendGrid
	fmt.Printf("[EMAIL] Sending OTP %s to %s\n", code, to)
}

func sendSMSOTP(to, code string, cfg *config.Config) {
	// Integrate Twilio or Fast2SMS (India)
	fmt.Printf("[SMS] Sending OTP %s to %s\n", code, to)
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(v)
}

func httpError(w http.ResponseWriter, msg string, code int) {
	http.Error(w, `{"error":"`+msg+`"}`, code)
}

// ValidateJWT is used by the WebSocket upgrader middleware
func ValidateJWT(tokenStr, secret string) (string, error) {
	token, err := jwt.Parse(tokenStr, func(t *jwt.Token) (any, error) {
		if _, ok := t.Method.(*jwt.SigningMethodHMAC); !ok {
			return nil, fmt.Errorf("unexpected signing method")
		}
		return []byte(secret), nil
	})
	if err != nil || !token.Valid {
		return "", fmt.Errorf("invalid token")
	}
	claims, _ := token.Claims.(jwt.MapClaims)
	return claims["sub"].(string), nil
}

// contextKey for storing userID in request context
type contextKey string

const UserIDKey contextKey = "userID"

// JWTMiddleware validates Bearer token and injects userID into context
func JWTMiddleware(secret string, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		authHeader := r.Header.Get("Authorization")
		if !strings.HasPrefix(authHeader, "Bearer ") {
			httpError(w, "missing token", 401)
			return
		}
		userID, err := ValidateJWT(strings.TrimPrefix(authHeader, "Bearer "), secret)
		if err != nil {
			httpError(w, "invalid token", 401)
			return
		}
		ctx := context.WithValue(r.Context(), UserIDKey, userID)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}
