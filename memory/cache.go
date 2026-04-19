package memory

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"time"

	"github.com/redis/go-redis/v9"
)

// SemanticCache is an exact-match cache. For similarity-based caching, swap the
// key derivation for an embedding lookup against RedisVL.
type SemanticCache struct {
	rdb       *redis.Client
	ttl       time.Duration
	keyPrefix string
}

type CacheOpts struct {
	TTL       time.Duration
	KeyPrefix string
}

func NewSemanticCache(rdb *redis.Client, opts CacheOpts) *SemanticCache {
	if opts.TTL == 0 {
		opts.TTL = 24 * time.Hour
	}
	if opts.KeyPrefix == "" {
		opts.KeyPrefix = "mem:cache:"
	}
	return &SemanticCache{rdb: rdb, ttl: opts.TTL, keyPrefix: opts.KeyPrefix}
}

func (c *SemanticCache) key(prompt string) string {
	sum := sha256.Sum256([]byte(prompt))
	return c.keyPrefix + hex.EncodeToString(sum[:16])
}

// GetOrCompute returns a cached answer when available; otherwise it calls fn,
// stores the result and returns it.
func (c *SemanticCache) GetOrCompute(ctx context.Context, prompt string, fn func() (string, error)) (string, error) {
	if v, err := c.rdb.Get(ctx, c.key(prompt)).Result(); err == nil {
		return v, nil
	}
	answer, err := fn()
	if err != nil {
		return "", err
	}
	_ = c.rdb.Set(ctx, c.key(prompt), answer, c.ttl).Err()
	return answer, nil
}

// Invalidate removes a cached response.
func (c *SemanticCache) Invalidate(ctx context.Context, prompt string) error {
	return c.rdb.Del(ctx, c.key(prompt)).Err()
}
