package cache

import (
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"

	"projectlens/internal/config"
)

func TestRedisSearchCache(t *testing.T) {
	server := miniredis.RunT(t)
	server.RequireAuth("example")
	cfg := config.RedisConfig{Addr: server.Addr(), Password: "example", DB: 2,
		DialTimeout: 100 * time.Millisecond, ReadTimeout: 100 * time.Millisecond, WriteTimeout: 100 * time.Millisecond}
	cache := NewRedisSearchCache(cfg)
	t.Cleanup(func() { _ = cache.Close() })
	ctx := t.Context()
	if err := cache.Ping(ctx); err != nil {
		t.Fatal(err)
	}

	const key = "search"
	if value, hit, err := cache.Get(ctx, key); value != nil || hit || err != nil {
		t.Fatalf("missing key = %q, %t, %v", value, hit, err)
	}
	if err := cache.Set(ctx, key, []byte("payload"), time.Minute); err != nil {
		t.Fatal(err)
	}
	if value, hit, err := cache.Get(ctx, key); string(value) != "payload" || !hit || err != nil {
		t.Fatalf("cached key = %q, %t, %v", value, hit, err)
	}
	if server.Exists(key) || !server.DB(2).Exists(key) || server.DB(2).TTL(key) != time.Minute {
		t.Fatal("cache key was written to the wrong database or with the wrong TTL")
	}
	server.FastForward(time.Minute)
	if _, hit, err := cache.Get(ctx, key); hit || err != nil {
		t.Fatalf("expired key = %t, %v", hit, err)
	}

	server.Close()
	if _, hit, err := cache.Get(ctx, key); hit || err == nil {
		t.Fatalf("unavailable Redis got hit=%t, error=%v", hit, err)
	}
	if err := cache.Set(ctx, key, []byte("offline"), time.Minute); err == nil {
		t.Fatal("unavailable Redis accepted write")
	}
	if err := server.Restart(); err != nil {
		t.Fatal(err)
	}
	if err := cache.Set(ctx, key, []byte("recovered"), time.Minute); err != nil {
		t.Fatal(err)
	}
	if value, hit, err := cache.Get(ctx, key); string(value) != "recovered" || !hit || err != nil {
		t.Fatalf("recovered key = %q, %t, %v", value, hit, err)
	}
}
