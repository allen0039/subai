package audit

import (
	"sync"
	"time"
)

// Cache stores moderation results keyed by HMAC of the normalized audit input
// plus scope and version bindings (§6.2, §21). Only HMACs and results are
// stored — never original text.
type Cache struct {
	mu    sync.RWMutex
	ttl   time.Duration
	items map[string]cacheItem
}

type cacheItem struct {
	result    CachedResult
	expiresAt time.Time
}

// CachedResult is what a cache hit restores.
type CachedResult struct {
	Decision     Decision
	Categories   map[string]any
	ModelVersion string
}

func NewCache(ttl time.Duration) *Cache {
	return &Cache{ttl: ttl, items: map[string]cacheItem{}}
}

// Key binds: content HMAC + key scope + ruleset version + moderation model +
// policy version (§6.2). Any version change invalidates by construction.
func CacheKey(contentHMAC []byte, keyScope, ruleVersion, moderationModel string, policyVer int64) string {
	return HexID(contentHMAC) + "|" + keyScope + "|" + ruleVersion + "|" + moderationModel + "|" + itoa(policyVer)
}

func (c *Cache) Get(key string) (CachedResult, bool) {
	c.mu.RLock()
	item, ok := c.items[key]
	c.mu.RUnlock()
	if !ok || time.Now().After(item.expiresAt) {
		return CachedResult{}, false
	}
	return item.result, true
}

func (c *Cache) Put(key string, r CachedResult) {
	c.mu.Lock()
	defer c.mu.Unlock()
	// bound memory: cheap approximate eviction when oversized
	if len(c.items) > 50_000 {
		now := time.Now()
		for k, v := range c.items {
			if now.After(v.expiresAt) {
				delete(c.items, k)
			}
		}
		if len(c.items) > 50_000 {
			c.items = map[string]cacheItem{} // last-resort clear, correctness unaffected
		}
	}
	c.items[key] = cacheItem{result: r, expiresAt: time.Now().Add(c.ttl)}
}

// Clear drops all cached allow decisions after a ruleset reload. Versioned keys
// are the primary guard; clearing makes a successful operator change immediate.
func (c *Cache) Clear() {
	c.mu.Lock()
	c.items = map[string]cacheItem{}
	c.mu.Unlock()
}

func itoa(n int64) string {
	if n == 0 {
		return "0"
	}
	var b [20]byte
	i := len(b)
	neg := n < 0
	if neg {
		n = -n
	}
	for n > 0 {
		i--
		b[i] = byte('0' + n%10)
		n /= 10
	}
	if neg {
		i--
		b[i] = '-'
	}
	return string(b[i:])
}
