package tllsp

import (
	"reflect"
	"sync"
	"testing"
)

func TestIndexTableSnapshotsAndPrefix(t *testing.T) {
	base := newIndexTable[int]()
	for _, name := range []string{"Box", "Item", "Item.Child", "Items", "Other"} {
		base.set(name, len(name))
	}
	var readers sync.WaitGroup
	for range 8 {
		readers.Add(1)
		go func() {
			defer readers.Done()
			for range 100 {
				// Independent writers fork the same published table while another
				// reader enumerates it. No receiver/node may be modified in place.
				next := base.clone()
				next.delete("Item")
				next.set("Item.New", 42)
				if next.get("Item") != 0 || next.get("Item.New") != 42 {
					t.Error("clone did not apply changes")
				}
				var names []string
				base.prefix("Item", func(name string, _ int) { names = append(names, name) })
				if !reflect.DeepEqual(names, []string{"Item", "Item.Child", "Items"}) || base.get("Item.New") != 0 {
					t.Errorf("published table changed: %v", names)
				}
			}
		}()
	}
	readers.Wait()
}
