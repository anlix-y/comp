package main

import (
	"context"
	"fmt"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/redis/go-redis/v9"

	"comp/internal/cleanup"
	cfgpkg "comp/internal/config"
	"comp/internal/httpapi"
	"comp/internal/logx"
	"comp/internal/store"
)

func main() {
	cfg, _ := cfgpkg.Load()
	logger, _ := logx.Init(cfg.LogLevel)
	if logger != nil {
		defer logger.Sync()
	}

	var rdb *redis.Client
	if cfg.RedisAddr != "" {
		rdb = redis.NewClient(&redis.Options{Addr: cfg.RedisAddr, DB: cfg.RedisDB, Password: cfg.RedisPassword})
		if err := rdb.Ping(context.Background()).Err(); err != nil {
			if logger != nil {
				logger.Warnf("Redis ping failed: %v (fallback to memory)", err)
			}
			rdb = nil
		}
	}
	st := store.NewRedisStore(rdb)

	deps := httpapi.Deps{Cfg: cfg, Logger: logger, Store: st, Redis: rdb}
	r := httpapi.NewRouter(deps)

	// Optional: trust proxy headers if behind reverse proxy
	gin.SetMode(gin.ReleaseMode)
	// Start cleanup for uploads dir; attempt both common locations
	cleanup.Start("uploads", cfg.CleanupMinutes)
	cleanup.Start("web/uploads", cfg.CleanupMinutes)
	addr := fmt.Sprintf("0.0.0.0:%d", cfg.Port)
	_ = r.Run(addr)
	// give logger time to flush
	time.Sleep(50 * time.Millisecond)
}
