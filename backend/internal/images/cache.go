// 本文件管理跨实例共享的 Redis 图片缓存：直接以 oss:// 引用为键保存原始字节，按绝对 TTL 过期并在图片键空间内按最近访问时间淘汰。

package images

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/OmniNetOpensource/aether-personal/backend/internal/services"
	"github.com/redis/go-redis/v9"
)

// 缓存淘汰索引统一放入同一哈希标签槽，与唤醒队列和用户租约完全隔离，避免全局内存淘汰误删协调信号。
const (
	imageCacheLRUKey   = "{image:cache}:lru"
	imageCacheSizesKey = "{image:cache}:sizes"
	imageCacheBytesKey = "{image:cache}:bytes"
)

// trackCachedImageLua 在同一哈希槽内原子登记新缓存图片的大小与访问时间，并在超出容量上限时弹出最久未访问的 oss:// 键。
const trackCachedImageLua = `
local oldSize = tonumber(redis.call("HGET", KEYS[2], ARGV[1]) or "0")
local newSize = tonumber(ARGV[2])
local maxBytes = tonumber(ARGV[3])
local nowMilli = tonumber(ARGV[4])

local totalBytes = tonumber(redis.call("GET", KEYS[3]) or "0")
if oldSize > 0 then
	redis.call("HDEL", KEYS[2], ARGV[1])
	redis.call("ZREM", KEYS[1], ARGV[1])
	totalBytes = totalBytes - oldSize
end

local evicted = {}
while totalBytes + newSize > maxBytes do
	local popped = redis.call("ZPOPMIN", KEYS[1], 1)
	if #popped == 0 then
		break
	end
	local victim = popped[1]
	local victimSize = tonumber(redis.call("HGET", KEYS[2], victim) or "0")
	redis.call("HDEL", KEYS[2], victim)
	totalBytes = totalBytes - victimSize
	evicted[#evicted + 1] = victim
end

redis.call("HSET", KEYS[2], ARGV[1], newSize)
redis.call("ZADD", KEYS[1], nowMilli, ARGV[1])
redis.call("SET", KEYS[3], totalBytes + newSize)
return evicted
`

// NewStore 创建共享 Redis 图片缓存的图片与文件存储；键到期由 Redis 自动释放，无需本地目录与后台清理协程。
func NewStore(serviceSettings *services.Store, redisClient *redis.Client, maxBytes int64, ttl time.Duration) *Store {
	return &Store{
		services:      serviceSettings,
		redisClient:   redisClient,
		cacheMaxBytes: maxBytes,
		cacheTTL:      ttl,
	}
}

// readCachedImage 直接以 oss:// 引用从 Redis 读取原始图片字节；仅将键不存在或已到期视为未命中，读取不延长绝对 TTL。
func (s *Store) readCachedImage(ctx context.Context, reference string) ([]byte, error) {
	data, err := s.redisClient.Get(ctx, reference).Bytes()
	if errors.Is(err, redis.Nil) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read cached image %s: %w", reference, err)
	}
	// 仅在淘汰索引仍记录该引用时更新最近访问时间，避免刚被并发淘汰的键重新写回有序集合。
	if err := s.redisClient.ZAddXX(ctx, imageCacheLRUKey, redis.Z{
		Score:  float64(time.Now().UnixMilli()),
		Member: reference,
	}).Err(); err != nil {
		return nil, fmt.Errorf("touch cached image %s: %w", reference, err)
	}
	return data, nil
}

// writeCachedImage 以 oss:// 引用将原始图片字节写入 Redis：NX 保留并发未命中时先发布的副本与原有效期，超限时按 LRU 淘汰旧图片。
func (s *Store) writeCachedImage(ctx context.Context, reference string, data []byte) error {
	inserted, err := s.redisClient.SetNX(ctx, reference, data, s.cacheTTL).Result()
	if err != nil {
		return fmt.Errorf("write cached image %s: %w", reference, err)
	}
	if !inserted {
		return nil
	}
	evicted, err := s.redisClient.Eval(
		ctx,
		trackCachedImageLua,
		[]string{imageCacheLRUKey, imageCacheSizesKey, imageCacheBytesKey},
		reference,
		len(data),
		s.cacheMaxBytes,
		time.Now().UnixMilli(),
	).StringSlice()
	if err != nil {
		return fmt.Errorf("track cached image %s: %w", reference, err)
	}
	if len(evicted) > 0 {
		if err := s.redisClient.Del(ctx, evicted...).Err(); err != nil {
			return fmt.Errorf("evict cached images: %w", err)
		}
	}
	return nil
}
