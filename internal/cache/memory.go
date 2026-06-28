package cache

import (
	"container/list"
	"sync"
	"time"
)

// entry is a single cache item stored in the LRU list.
type entry struct {
	key       string
	value     []byte
	expiresAt time.Time
}

// Memory is a thread-safe in-memory LRU cache with per-entry TTL.
type Memory struct {
	mu         sync.Mutex
	maxEntries int
	items      map[string]*list.Element
	evictList  *list.List
	now        func() time.Time // injectable clock for testing
}

// NewMemory creates an in-memory LRU cache with the given maximum number of entries.
func NewMemory(maxEntries int) *Memory {
	return &Memory{
		maxEntries: maxEntries,
		items:      make(map[string]*list.Element),
		evictList:  list.New(),
		now:        time.Now,
	}
}

// Get retrieves a cached value by key. Returns nil if not found or expired.
func (m *Memory) Get(key string) []byte {
	m.mu.Lock()
	defer m.mu.Unlock()

	el, ok := m.items[key]
	if !ok {
		return nil
	}

	ent := el.Value.(*entry) //nolint:errcheck // evictList only ever holds *entry
	if m.now().After(ent.expiresAt) {
		m.removeElement(el)
		return nil
	}

	m.evictList.MoveToFront(el)
	return ent.value
}

// Set stores a value with the given key and TTL. If the key already exists,
// it is updated and moved to the front. If the cache is full, the least
// recently used entry is evicted.
func (m *Memory) Set(key string, value []byte, ttl time.Duration) {
	m.mu.Lock()
	defer m.mu.Unlock()

	if el, ok := m.items[key]; ok {
		ent := el.Value.(*entry) //nolint:errcheck // evictList only ever holds *entry
		ent.value = value
		ent.expiresAt = m.now().Add(ttl)
		m.evictList.MoveToFront(el)
		return
	}

	ent := &entry{
		key:       key,
		value:     value,
		expiresAt: m.now().Add(ttl),
	}
	el := m.evictList.PushFront(ent)
	m.items[key] = el

	if m.evictList.Len() > m.maxEntries {
		m.removeOldest()
	}
}

// Len returns the number of entries in the cache (including expired but not yet evicted).
func (m *Memory) Len() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.evictList.Len()
}

func (m *Memory) removeOldest() {
	el := m.evictList.Back()
	if el != nil {
		m.removeElement(el)
	}
}

func (m *Memory) removeElement(el *list.Element) {
	m.evictList.Remove(el)
	ent := el.Value.(*entry) //nolint:errcheck // evictList only ever holds *entry
	delete(m.items, ent.key)
}
