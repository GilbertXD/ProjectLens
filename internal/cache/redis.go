package cache

import (
	"context"
	"errors"
	"time"

	"github.com/redis/go-redis/v9"

	"projectlens/internal/config"
)

type RedisSearchCache struct {
	client *redis.Client
}

func NewRedisSearchCache(cfg config.RedisConfig) *RedisSearchCache {
	return newRedisSearchCache(&redis.Options{Addr: cfg.Addr, Password: cfg.Password, DB: cfg.DB}, cfg)
}

func NewRedisSearchCacheFromURL(rawURL string, cfg config.RedisConfig) (*RedisSearchCache, error) {
	options, err := redis.ParseURL(rawURL)
	if err != nil {
		return nil, err
	}
	return newRedisSearchCache(options, cfg), nil
}

func newRedisSearchCache(options *redis.Options, cfg config.RedisConfig) *RedisSearchCache {
	// go-redis uses -1 for no command retries and counts the initial dial as an attempt.
	options.MaxRetries = -1
	options.DialerRetries = 1
	options.DialerRetryTimeout = time.Nanosecond // The pool applies this delay even after its final dial.
	options.DialTimeout = cfg.DialTimeout
	options.ReadTimeout = cfg.ReadTimeout
	options.WriteTimeout = cfg.WriteTimeout
	options.PoolTimeout = cfg.ReadTimeout
	options.ContextTimeoutEnabled = true
	options.Protocol = 2
	options.DisableIdentity = true
	return &RedisSearchCache{client: redis.NewClient(options)}
}

func (c *RedisSearchCache) Ping(ctx context.Context) error {
	return c.client.Ping(ctx).Err()
}

func (c *RedisSearchCache) Get(ctx context.Context, key string) ([]byte, bool, error) {
	value, err := c.client.Get(ctx, key).Bytes()
	if errors.Is(err, redis.Nil) {
		return nil, false, nil
	}
	return value, err == nil, err
}

func (c *RedisSearchCache) Set(ctx context.Context, key string, value []byte, ttl time.Duration) error {
	return c.client.Set(ctx, key, value, ttl).Err()
}

func (c *RedisSearchCache) Close() error {
	return c.client.Close()
}
