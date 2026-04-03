package cache

import (
	"context"
	"fmt"
	"time"

	"github.com/redis/go-redis/v9"
)

// Redis implements the Cache interface backed by a Redis server.
type Redis struct {
	client    *redis.Client
	keyPrefix string
}

// NewRedis creates a Redis-backed cache and verifies the connection with a PING.
func NewRedis(addr, password string, db int, keyPrefix string) (*Redis, error) {
	client := redis.NewClient(&redis.Options{
		Addr:     addr,
		Password: password,
		DB:       db,
	})

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	if err := client.Ping(ctx).Err(); err != nil {
		_ = client.Close()
		return nil, fmt.Errorf("redis ping failed: %w", err)
	}

	return &Redis{
		client:    client,
		keyPrefix: keyPrefix,
	}, nil
}

// Get retrieves a cached value by key. Returns nil if not found or expired.
func (r *Redis) Get(key string) []byte {
	val, err := r.client.Get(context.Background(), r.keyPrefix+key).Bytes()
	if err != nil {
		return nil // miss or error
	}
	return val
}

// Set stores a value with the given key and TTL.
func (r *Redis) Set(key string, value []byte, ttl time.Duration) {
	_ = r.client.Set(context.Background(), r.keyPrefix+key, value, ttl).Err()
}

// Len returns the number of keys in the current Redis database.
// Note: when using a shared Redis instance, this counts all keys, not just Butter's.
func (r *Redis) Len() int {
	val, err := r.client.DBSize(context.Background()).Result()
	if err != nil {
		return 0
	}
	return int(val)
}

// Close releases the Redis connection.
func (r *Redis) Close() error {
	return r.client.Close()
}
