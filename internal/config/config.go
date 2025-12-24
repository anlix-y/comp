package config

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

type Config struct {
	Proxy          string `json:"proxy"`
	CleanupMinutes int    `json:"cleanup_minutes"`
	Port           int    `json:"port"`
	RedisAddr      string `json:"redis_addr"`
	RedisDB        int    `json:"redis_db"`
	RedisPassword  string `json:"redis_password"`
	LogLevel       string `json:"log_level"`
}

func Load() (Config, error) {
	// Defaults
	cfg := Config{
		Port:           3000,
		CleanupMinutes: 5,
		RedisAddr:      "localhost:6379",
		RedisDB:        0,
		LogLevel:       "info",
	}

	paths := []string{"config.json", filepath.Join("web", "config.json")}
	for _, p := range paths {
		f, err := os.Open(p)
		if err != nil {
			continue
		}
		defer f.Close()
		dec := json.NewDecoder(f)
		if err := dec.Decode(&cfg); err != nil {
			return cfg, fmt.Errorf("parse config %s: %w", p, err)
		}
		break
	}
	cfg.Proxy = strings.TrimSpace(cfg.Proxy)
	return cfg, nil
}
