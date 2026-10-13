package tllsp

import (
	"strings"

	"github.com/google/btree"
)

type tableItem[V any] struct {
	key   string
	value V
}

// indexTable keeps ordered lookups without copying or sorting the entire name
// index on an edit. Values and their nested slices/maps are immutable once the
// table is published; callers replace a value before changing its contents.
type indexTable[V any] struct{ tree *btree.BTreeG[tableItem[V]] }

func newIndexTable[V any]() indexTable[V] {
	return indexTable[V]{btree.NewG(16, func(a, b tableItem[V]) bool { return a.key < b.key })}
}

func (t indexTable[V]) clone() indexTable[V] {
	// Clone changes its receiver's copy-on-write context. Use a private shallow
	// copy so readers (and other clones) never access a modified tree object.
	copy := *t.tree
	return indexTable[V]{copy.Clone()}
}

func (t indexTable[V]) get(key string) V {
	item, _ := t.tree.Get(tableItem[V]{key: key})
	return item.value
}

func (t indexTable[V]) set(key string, value V) {
	t.tree.ReplaceOrInsert(tableItem[V]{key, value})
}

func (t indexTable[V]) delete(key string) { t.tree.Delete(tableItem[V]{key: key}) }

func (t indexTable[V]) each(visit func(string, V)) {
	t.tree.Ascend(func(item tableItem[V]) bool { visit(item.key, item.value); return true })
}

func (t indexTable[V]) prefix(prefix string, visit func(string, V)) {
	t.prefixWhile(prefix, func(key string, value V) bool { visit(key, value); return true })
}

func (t indexTable[V]) prefixWhile(prefix string, visit func(string, V) bool) {
	t.tree.AscendGreaterOrEqual(tableItem[V]{key: prefix}, func(item tableItem[V]) bool {
		if !strings.HasPrefix(item.key, prefix) {
			return false
		}
		return visit(item.key, item.value)
	})
}
