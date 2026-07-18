package gateway

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/redis/go-redis/v9"
)

// RedisRateLimitBackend coordinates a fixed-window request quota across
// gateway replicas. The Lua script creates the window and increments its
// counter atomically, so concurrent replicas cannot over-admit a request.
type RedisRateLimitBackend struct {
	client *redis.Client
	prefix string
	script *redis.Script
}

func NewRedisRateLimitBackend(redisURL, prefix string) (*RedisRateLimitBackend, error) {
	options, err := redis.ParseURL(strings.TrimSpace(redisURL))
	if err != nil {
		return nil, fmt.Errorf("parse Redis URL: %w", err)
	}
	prefix = strings.TrimSpace(prefix)
	if prefix == "" {
		prefix = "omniswitch:ratelimit"
	}
	return &RedisRateLimitBackend{
		client: redis.NewClient(options),
		prefix: prefix,
		script: redis.NewScript(`
local count = redis.call('INCR', KEYS[1])
if count == 1 then
  redis.call('PEXPIRE', KEYS[1], ARGV[1])
end
local ttl = redis.call('PTTL', KEYS[1])
if count > tonumber(ARGV[2]) then
  return {0, ttl}
end
return {1, ttl}`),
	}, nil
}

func (backend *RedisRateLimitBackend) Allow(ctx context.Context, key string, limit int, interval time.Duration) (bool, time.Duration, error) {
	if limit <= 0 || interval <= 0 {
		return true, 0, nil
	}
	intervalMilliseconds := max(int64(1), interval.Milliseconds())
	window := time.Now().UTC().UnixMilli() / intervalMilliseconds
	redisKey := fmt.Sprintf("%s:%d:%s", backend.prefix, window, key)
	values, err := backend.script.Run(ctx, backend.client, []string{redisKey}, intervalMilliseconds, limit).Int64Slice()
	if err != nil {
		return false, 0, fmt.Errorf("run Redis rate-limit script: %w", err)
	}
	if len(values) != 2 {
		return false, 0, fmt.Errorf("invalid Redis rate-limit script result")
	}
	return values[0] == 1, time.Duration(values[1]) * time.Millisecond, nil
}

// Ping verifies that Redis is reachable before the gateway starts serving
// requests. This avoids a healthy-looking process that rejects every request
// in fail-closed mode after its first live call.
func (backend *RedisRateLimitBackend) Ping(ctx context.Context) error {
	if err := backend.client.Ping(ctx).Err(); err != nil {
		return fmt.Errorf("ping Redis: %w", err)
	}
	return nil
}

func (backend *RedisRateLimitBackend) Close() error {
	return backend.client.Close()
}
