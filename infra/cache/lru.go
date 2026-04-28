package cache

import lru "github.com/hashicorp/golang-lru/v2"

// LRU wraps the underlying cache implementation so shared cache behavior
// can be reused across HTTP adapters.
type LRU[K comparable, V any] struct {
	cache *lru.Cache[K, V]
}

// NewLRU creates an LRU cache with the given capacity. If size is <= 0, defaults to 1024.
func NewLRU[K comparable, V any](size int) (*LRU[K, V], error) {
	if size <= 0 {
		size = 1024
	}
	c, err := lru.New[K, V](size)
	if err != nil {
		return nil, err
	}
	return &LRU[K, V]{cache: c}, nil
}

func (l *LRU[K, V]) Get(key K) (V, bool) {
	if l == nil || l.cache == nil {
		var zero V
		return zero, false
	}
	return l.cache.Get(key)
}

func (l *LRU[K, V]) Add(key K, value V) {
	if l == nil || l.cache == nil {
		return
	}
	l.cache.Add(key, value)
}

func (l *LRU[K, V]) Remove(key K) {
	if l == nil || l.cache == nil {
		return
	}
	l.cache.Remove(key)
}
