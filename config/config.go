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
	"strconv"
)

type Config struct {
	// Server
	Port string
	Env  string // "development" | "production"

	// Database
	DatabaseURL string
	DBMaxOpen   int
	DBMaxIdle   int

	// Redis
	RedisURL      string
	RedisPassword string

	// Auth
	JWTSecret     string
	OTPExpirySecs int // default 300 (5 min)

	// Email OTP
	SMTPHost string
	SMTPPort string
	SMTPUser string
	SMTPPass string
	SMTPFrom string

	// SMS OTP (Twilio or Fast2SMS for India)
	TwilioSID   string
	TwilioToken string
	TwilioFrom  string

	// PDF export
	WkhtmltopdfPath string // path to wkhtmltopdf binary
}

// GetJWTSecret implements the interface expected by ws.WSHandler
// so we don't import config into ws (avoids circular deps).
func (c *Config) GetJWTSecret() string { return c.JWTSecret }

// IsProduction returns true when running in production mode.
func (c *Config) IsProduction() bool { return c.Env == "production" }

func Load() *Config {
	return &Config{
		Port:            getEnv("PORT", ":8080"),
		Env:             getEnv("ENV", "development"),
		DatabaseURL:     requireEnv("DATABASE_URL"),
		DBMaxOpen:       getEnvInt("DB_MAX_OPEN", 25),
		DBMaxIdle:       getEnvInt("DB_MAX_IDLE", 10),
		RedisURL:        getEnv("REDIS_URL", "localhost:6379"),
		RedisPassword:   getEnv("REDIS_PASSWORD", ""),
		JWTSecret:       requireEnv("JWT_SECRET"),
		OTPExpirySecs:   getEnvInt("OTP_EXPIRY_SECS", 300),
		SMTPHost:        getEnv("SMTP_HOST", "smtp.gmail.com"),
		SMTPPort:        getEnv("SMTP_PORT", "587"),
		SMTPUser:        getEnv("SMTP_USER", ""),
		SMTPPass:        getEnv("SMTP_PASS", ""),
		SMTPFrom:        getEnv("SMTP_FROM", "noreply@equalink.app"),
		TwilioSID:       getEnv("TWILIO_SID", ""),
		TwilioToken:     getEnv("TWILIO_TOKEN", ""),
		TwilioFrom:      getEnv("TWILIO_FROM", ""),
		WkhtmltopdfPath: getEnv("WKHTMLTOPDF_PATH", "/usr/bin/wkhtmltopdf"),
	}
}

func getEnv(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

func getEnvInt(key string, fallback int) int {
	if v := os.Getenv(key); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			return n
		}
	}
	return fallback
}

// requireEnv crashes at startup if a critical env var is missing.
// WHY crash? Better to fail loudly at boot than silently serve errors.
func requireEnv(key string) string {
	v := os.Getenv(key)
	if v == "" {
		log.Fatalf("FATAL: required environment variable %q is not set.\nHint: copy .env.example to .env and fill in the values.", key)
	}
	return v
}
