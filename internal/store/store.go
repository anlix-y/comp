package store

import (
	"context"
	"encoding/json"
	"sync"
	"time"

	redis "github.com/redis/go-redis/v9"
)

type TaskStatus struct {
	ID         string `json:"id"`
	Status     string `json:"status"`
	Error      string `json:"error,omitempty"`
	OutputFile string `json:"output_file,omitempty"`
	Stage      string `json:"stage,omitempty"`
	Percent    int    `json:"percent,omitempty"`
}

type Store interface {
	Set(ctx context.Context, t *TaskStatus, ttl time.Duration) error
	Get(ctx context.Context, id string) (*TaskStatus, bool)
}

// Redis-backed store with graceful fallback to memory when redis is nil.
type RedisStore struct {
	Rdb *redis.Client
	mem *MemoryStore
}

func NewRedisStore(rdb *redis.Client) *RedisStore {
	return &RedisStore{Rdb: rdb, mem: NewMemoryStore()}
}

func (s *RedisStore) Set(ctx context.Context, t *TaskStatus, ttl time.Duration) error {
	if s.Rdb == nil {
		return s.mem.Set(ctx, t, ttl)
	}
	b, _ := json.Marshal(t)
	return s.Rdb.Set(ctx, "task:"+t.ID, b, ttl).Err()
}

func (s *RedisStore) Get(ctx context.Context, id string) (*TaskStatus, bool) {
	if s.Rdb == nil {
		return s.mem.Get(ctx, id)
	}
	v, err := s.Rdb.Get(ctx, "task:"+id).Result()
	if err != nil {
		return nil, false
	}
	var t TaskStatus
	if json.Unmarshal([]byte(v), &t) != nil {
		return nil, false
	}
	return &t, true
}

// Simple in-memory implementation
type MemoryStore struct {
	mu   sync.RWMutex
	data map[string]TaskStatus
}

func NewMemoryStore() *MemoryStore { return &MemoryStore{data: make(map[string]TaskStatus)} }

func (m *MemoryStore) Set(_ context.Context, t *TaskStatus, _ time.Duration) error {
	m.mu.Lock()
	m.data[t.ID] = *t
	m.mu.Unlock()
	return nil
}

func (m *MemoryStore) Get(_ context.Context, id string) (*TaskStatus, bool) {
	m.mu.RLock()
	v, ok := m.data[id]
	m.mu.RUnlock()
	if !ok {
		return nil, false
	}
	vv := v
	return &vv, true
}
