package driver

import (
	"container/list"
	"crypto/sha256"
	"encoding/binary"
	"sync"
)

// A decision memo remembers the outcome of a pure, deterministic check of daemon-supplied bytes.
//
// The relay path inspects the same container, and lists the same inventory, several times per relayed
// request, and each inspection walks the whole document with a strict decoder before comparing it with the
// plan. That work is a function of nothing but the document bytes and the plan, so identical bytes under the
// same plan have the same answer and nothing needs invalidating: a changed container, label, mount or
// state is a different document, hence a different key, and is validated from scratch.
//
// Only successes are stored. A refusal, an error or an unknown state is always recomputed, so the memo can
// shorten a pass that would have succeeded and can never turn a failure into a success. Nothing about a
// remembered answer is trusted beyond the bytes it was derived from: the live document is still fetched
// from the daemon on every call.
type memoKey [sha256.Size]byte

type memoEntry[V any] struct {
	key   memoKey
	value V
}

type decisionMemo[V any] struct {
	mu           sync.Mutex
	capacity     int
	order        *list.List // front = most recently used
	entries      map[memoKey]*list.Element
	hits, misses uint64
}

func newDecisionMemo[V any](capacity int) *decisionMemo[V] {
	return &decisionMemo[V]{capacity: capacity, order: list.New(), entries: map[memoKey]*list.Element{}}
}

func (m *decisionMemo[V]) get(key memoKey) (V, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if el, ok := m.entries[key]; ok {
		m.order.MoveToFront(el)
		m.hits++
		return el.Value.(*memoEntry[V]).value, true
	}
	m.misses++
	var zero V
	return zero, false
}

func (m *decisionMemo[V]) put(key memoKey, value V) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if el, ok := m.entries[key]; ok {
		el.Value.(*memoEntry[V]).value = value
		m.order.MoveToFront(el)
		return
	}
	m.entries[key] = m.order.PushFront(&memoEntry[V]{key: key, value: value})
	for m.order.Len() > m.capacity {
		oldest := m.order.Back()
		m.order.Remove(oldest)
		delete(m.entries, oldest.Value.(*memoEntry[V]).key)
	}
}

func (m *decisionMemo[V]) stats() (hits, misses uint64, size int) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.hits, m.misses, m.order.Len()
}

// memoKeyOf digests length-prefixed parts, so no two part lists can collide by concatenation.
func memoKeyOf(parts ...[]byte) memoKey {
	h := sha256.New()
	for _, part := range parts {
		var n [8]byte
		binary.BigEndian.PutUint64(n[:], uint64(len(part)))
		h.Write(n[:])
		h.Write(part)
	}
	var key memoKey
	h.Sum(key[:0])
	return key
}
