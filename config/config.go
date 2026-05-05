package config

/*

  config.go — All Environment Variables

  WHY a struct instead of os.Getenv everywhere?
  - Fail fast at startup (missing DB URL = crash now,
    not 3 requests later when the first DB call hits)
  - Single source of truth — pass *Config, not 10 args
  - Easy to mock in tests (swap Config values)

*/

import (
	"log"
	"os"
)

type Config struct {
	Port          string
	Env           string
	DatabaseURL   string
	RedisURL      string
	RedisPassword string
	JWTSecret     string
	SMTPHost      string
	SMTPPort      string
	SMTPUser      string
	SMTPPass      string
	TwilioSID     string // for SMS OTP
	TwilioToken   string
	TwilioFrom    string
}

// GetJWTSecret implements the interface expected by ws.WSHandler
func (c *Config) GetJWTSecret() string { return c.JWTSecret }

func Load() *Config {
	cfg := &Config{
		Port:          getEnv("PORT", ":8080"),
		Env:           getEnv("ENV", "development"),
		DatabaseURL:   requireEnv("DATABASE_URL"),
		RedisURL:      getEnv("REDIS_URL", "localhost:6379"),
		RedisPassword: getEnv("REDIS_PASSWORD", ""),
		JWTSecret:     requireEnv("JWT_SECRET"),
		SMTPHost:      getEnv("SMTP_HOST", "smtp.gmail.com"),
		SMTPPort:      getEnv("SMTP_PORT", "587"),
		SMTPUser:      getEnv("SMTP_USER", ""),
		SMTPPass:      getEnv("SMTP_PASS", ""),
		TwilioSID:     getEnv("TWILIO_SID", ""),
		TwilioToken:   getEnv("TWILIO_TOKEN", ""),
		TwilioFrom:    getEnv("TWILIO_FROM", ""),
	}
	return cfg
}

func getEnv(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

// requireEnv crashes at startup if a critical env var is missing.
// WHY crash? A server running without a DB URL will fail every request.
// Better to know immediately than to serve 500s silently.
func requireEnv(key string) string {
	v := os.Getenv(key)
	if v == "" {
		log.Fatalf("FATAL: required env var %q is not set", key)
	}
	return v
}
