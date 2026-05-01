package config

import "os"

type Config struct {
	Port        string
	DatabaseURL string
	RedisURL    string
}

func Load() *Config {
	return &Config{
		Port:        getEnv("PORT", ":8080"),
		DatabaseURL: getEnv("DATABASE_URL", "postgres://localhost/equalink?sslmode=disable"),
		RedisURL:    getEnv("REDIS_URL", "redis://localhost:6379"),
	}
}

func getEnv(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}
