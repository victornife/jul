package cache

import (
	"container/list"
	"sync"
)

// memStore is a size-bounded LRU cache. When inserting would exceed the byte
// cap, the least-recently-used entries are evicted; an optional onEvict hook
// receives them so they can overflow to the disk tier.
type memStore struct {
	mu       sync.Mutex
	maxBytes int64
	curBytes int64
	ll       *list.List // front = most recently used
	items    map[string]*list.Element
	onEvict  func(key string, e *Entry)
}

type memItem struct {
	key   string
	entry *Entry
	size  int64
}

func newMemStore(maxBytes int64, onEvict func(key string, e *Entry)) *memStore {
	if maxBytes <= 0 {
		maxBytes = 64 << 20
	}
	return &memStore{
		maxBytes: maxBytes,
		ll:       list.New(),
		items:    make(map[string]*list.Element),
		onEvict:  onEvict,
	}
}

func (m *memStore) get(key string) (*Entry, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	el, ok := m.items[key]
	if !ok {
		return nil, false
	}
	m.ll.MoveToFront(el)
	return el.Value.(*memItem).entry, true
}

func (m *memStore) set(key string, e *Entry) {
	size := e.Size()
	m.mu.Lock()
	defer m.mu.Unlock()

	if el, ok := m.items[key]; ok {
		it := el.Value.(*memItem)
		m.curBytes += size - it.size
		it.entry = e
		it.size = size
		m.ll.MoveToFront(el)
	} else {
		el := m.ll.PushFront(&memItem{key: key, entry: e, size: size})
		m.items[key] = el
		m.curBytes += size
	}
	m.evictLocked()
}

// evictLocked drops LRU entries until within the byte cap, forwarding each to
// onEvict (disk overflow).
func (m *memStore) evictLocked() {
	for m.curBytes > m.maxBytes && m.ll.Len() > 0 {
		el := m.ll.Back()
		it := el.Value.(*memItem)
		m.ll.Remove(el)
		delete(m.items, it.key)
		m.curBytes -= it.size
		if m.onEvict != nil {
			m.onEvict(it.key, it.entry)
		}
	}
}

func (m *memStore) del(key string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if el, ok := m.items[key]; ok {
		it := el.Value.(*memItem)
		m.ll.Remove(el)
		delete(m.items, it.key)
		m.curBytes -= it.size
	}
}

func (m *memStore) purge() {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.ll.Init()
	m.items = make(map[string]*list.Element)
	m.curBytes = 0
}
