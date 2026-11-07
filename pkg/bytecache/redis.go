package bytecache

import (
	"context"
	"fmt"
	"time"

	"github.com/redis/go-redis/v9"
)

// Redis is a Store backed by Redis. Keys get a prefix (one Redis can serve several
// installations) and a TTL; Redis itself should run with an LRU eviction policy.
type Redis struct {
	client redis.UniversalClient
	prefix string
	ttl    time.Duration
}

var _ Store = (*Redis)(nil)

// NewRedis wraps client. ttl 0 keeps keys until Redis evicts them.
func NewRedis(client redis.UniversalClient, prefix string, ttl time.Duration) *Redis {
	return &Redis{client: client, prefix: prefix, ttl: ttl}
}

// OpenRedis connects to url (redis://[user:pass@]host:port/db or rediss:// for TLS) and pings it.
// The client is returned even when the ping fails: it reconnects by itself, and callers
// fall back to the source of truth meanwhile.
func OpenRedis(ctx context.Context, url string) (*redis.Client, error) {
	opt, err := redis.ParseURL(url)
	if err != nil {
		return nil, fmt.Errorf("parse REDIS_URL: %w", err)
	}
	// a cache must never make a request slow: fail fast and read the database instead
	opt.DialTimeout, opt.ReadTimeout, opt.WriteTimeout = time.Second, 500*time.Millisecond, 500*time.Millisecond
	client := redis.NewClient(opt)
	ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	if err := client.Ping(ctx).Err(); err != nil {
		return client, fmt.Errorf("ping redis: %w", err)
	}
	return client, nil
}

// GetMulti reads keys with one MGET.
func (r *Redis) GetMulti(ctx context.Context, keys []string) (map[string][]byte, error) {
	if len(keys) == 0 {
		return map[string][]byte{}, nil
	}
	full := make([]string, len(keys))
	for i, k := range keys {
		full[i] = r.prefix + k
	}
	vals, err := r.client.MGet(ctx, full...).Result()
	if err != nil {
		return nil, fmt.Errorf("redis mget: %w", err)
	}
	out := make(map[string][]byte, len(keys))
	for i, v := range vals {
		if s, ok := v.(string); ok {
			out[keys[i]] = []byte(s)
		}
	}
	return out, nil
}

// SetMulti writes items in one pipeline.
func (r *Redis) SetMulti(ctx context.Context, items map[string][]byte) error {
	if len(items) == 0 {
		return nil
	}
	_, err := r.client.Pipelined(ctx, func(p redis.Pipeliner) error {
		for k, v := range items {
			p.Set(ctx, r.prefix+k, v, r.ttl)
		}
		return nil
	})
	if err != nil {
		return fmt.Errorf("redis set: %w", err)
	}
	return nil
}
