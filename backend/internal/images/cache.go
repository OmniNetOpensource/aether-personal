// 本文件管理图片的持久化磁盘缓存：按绝对有效期清理、按最近使用时间淘汰，内存只保留索引，下载不持有缓存锁。

package images

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/OmniNetOpensource/aether-personal/backend/internal/services"
)

// cacheEntry 的 writtenAt 来自文件修改时间，读取不改它，保证七天有效期不会随访问延长。
// lastUsedAt 只在内存里维护；重启后以写入时间恢复初始淘汰顺序，不影响有效期。
type cacheEntry struct {
	size       int64
	writtenAt  time.Time
	lastUsedAt time.Time
}

// NewStore 恢复专用缓存目录的索引，并清理进程退出留下的临时文件、过期图片和超出容量的旧缓存。
func NewStore(serviceSettings *services.Store, directory string, maxBytes int64, ttl time.Duration) (*Store, error) {
	if err := os.MkdirAll(directory, 0700); err != nil {
		return nil, fmt.Errorf("create image cache: %w", err)
	}
	files, err := os.ReadDir(directory)
	if err != nil {
		return nil, fmt.Errorf("read image cache directory: %w", err)
	}
	store := &Store{services: serviceSettings, cacheDirectory: directory, cacheMaxBytes: maxBytes,
		cacheTTL: ttl, cacheEntries: map[string]cacheEntry{}}
	// 正式文件只使用 .img 后缀；临时文件从不作为缓存命中，重启时移除未发布的副本。
	for _, file := range files {
		name := file.Name()
		if strings.HasPrefix(name, ".image-") {
			if err := os.Remove(filepath.Join(directory, name)); err != nil {
				return nil, fmt.Errorf("remove unfinished image cache: %w", err)
			}
			continue
		}
		if !strings.HasSuffix(name, ".img") {
			continue
		}
		info, err := file.Info()
		if err != nil {
			return nil, fmt.Errorf("inspect image cache: %w", err)
		}
		store.cacheEntries[name] = cacheEntry{size: info.Size(), writtenAt: info.ModTime(), lastUsedAt: info.ModTime()}
		store.cacheBytes += info.Size()
	}
	if err := store.cleanExpiredImages(); err != nil {
		return nil, err
	}
	if err := store.trimCache(0); err != nil {
		return nil, err
	}
	return store, nil
}

// cacheName 以完整 OSS 引用生成固定文件名，避免不同 Bucket 或用户的同名对象共用副本。
func cacheName(reference string) string {
	return fmt.Sprintf("%x.img", sha256.Sum256([]byte(reference)))
}

// readCachedImage 只把不存在或已过期视为未命中；文件系统错误直接返回，不用 OSS 下载掩盖磁盘故障。
func (s *Store) readCachedImage(reference string) ([]byte, error) {
	s.cacheMutex.Lock()
	defer s.cacheMutex.Unlock()
	name := cacheName(reference)
	entry, exists := s.cacheEntries[name]
	if !exists {
		return nil, nil
	}
	if time.Since(entry.writtenAt) >= s.cacheTTL {
		return nil, s.removeCachedImage(name)
	}
	data, err := os.ReadFile(filepath.Join(s.cacheDirectory, name))
	if errors.Is(err, os.ErrNotExist) {
		delete(s.cacheEntries, name)
		s.cacheBytes -= entry.size
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read cached image %s: %w", reference, err)
	}
	entry.lastUsedAt = time.Now()
	s.cacheEntries[name] = entry
	return data, nil
}

// writeCachedImage 在锁内淘汰并发布完整文件；下载在调用它之前完成，不让慢网络阻塞其他用户读缓存。
func (s *Store) writeCachedImage(reference string, data []byte) (err error) {
	s.cacheMutex.Lock()
	defer s.cacheMutex.Unlock()
	name := cacheName(reference)
	// 两个请求同时未命中时，保留先发布的副本及其原始有效期。
	if entry, exists := s.cacheEntries[name]; exists {
		if time.Since(entry.writtenAt) < s.cacheTTL {
			return nil
		}
		if err := s.removeCachedImage(name); err != nil {
			return err
		}
	}
	if err := s.trimCache(int64(len(data))); err != nil {
		return err
	}
	file, err := os.CreateTemp(s.cacheDirectory, ".image-")
	if err != nil {
		return fmt.Errorf("create cached image %s: %w", reference, err)
	}
	// 失败时删除临时文件，清理失败也随原始错误返回；成功重命名后目录里只剩正式文件。
	defer func() {
		if err != nil {
			err = errors.Join(err, os.Remove(file.Name()))
		}
	}()
	_, writeErr := file.Write(data)
	if err = errors.Join(writeErr, file.Close()); err != nil {
		return fmt.Errorf("write cached image %s: %w", reference, err)
	}
	if err = os.Rename(file.Name(), filepath.Join(s.cacheDirectory, name)); err != nil {
		return fmt.Errorf("publish cached image %s: %w", reference, err)
	}
	now := time.Now()
	s.cacheEntries[name] = cacheEntry{size: int64(len(data)), writtenAt: now, lastUsedAt: now}
	s.cacheBytes += int64(len(data))
	return nil
}

// removeCachedImage 同步删除磁盘文件与索引；调用方持有缓存锁，或仍处在启动装配阶段。
func (s *Store) removeCachedImage(name string) error {
	if err := os.Remove(filepath.Join(s.cacheDirectory, name)); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("remove cached image %s: %w", name, err)
	}
	s.cacheBytes -= s.cacheEntries[name].size
	delete(s.cacheEntries, name)
	return nil
}

// trimCache 为新文件腾出空间，每次删除最近最少使用的副本；调用方持锁，启动恢复时尚无并发访问。
func (s *Store) trimCache(requiredBytes int64) error {
	for s.cacheBytes+requiredBytes > s.cacheMaxBytes {
		oldestName := ""
		var oldestTime time.Time
		for name, entry := range s.cacheEntries {
			if oldestName == "" || entry.lastUsedAt.Before(oldestTime) {
				oldestName, oldestTime = name, entry.lastUsedAt
			}
		}
		if err := s.removeCachedImage(oldestName); err != nil {
			return err
		}
	}
	return nil
}

// cleanExpiredImages 删除已过绝对有效期的文件，不触碰 OSS 原图。
func (s *Store) cleanExpiredImages() error {
	s.cacheMutex.Lock()
	defer s.cacheMutex.Unlock()
	for name, entry := range s.cacheEntries {
		if time.Since(entry.writtenAt) >= s.cacheTTL {
			if err := s.removeCachedImage(name); err != nil {
				return err
			}
		}
	}
	return nil
}

// RunCacheCleanup 由进程入口管理生命周期；退出时停止计时器，清理错误向上交给入口统一处理。
func (s *Store) RunCacheCleanup(ctx context.Context) error {
	ticker := time.NewTicker(time.Hour)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
			if err := s.cleanExpiredImages(); err != nil {
				return err
			}
		}
	}
}
