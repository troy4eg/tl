package tlast

import (
	"maps"
	"math/rand"
	"reflect"
	"slices"
	"sync"
	"testing"
)

func indexTestDeclarations(t *testing.T, id, tl, tl2 string) SchemaDeclarations {
	t.Helper()
	legacy, errors := ParseTLFileRecovering(tl, "/schema/"+id+".tl")
	modern, errors2 := ParseTL2FileRecovering(tl2, "/schema/"+id+".tl2")
	if len(errors)+len(errors2) != 0 {
		t.Fatalf("invalid fixture: %v %v", errors, errors2)
	}
	return SchemaDeclarations{legacy, modern.Combinators}
}

func checkSchemaIndex(t *testing.T, index *SchemaIndex, files map[string]SchemaDeclarations) {
	t.Helper()
	ids := slices.Sorted(maps.Keys(files))
	ordered := make([]SchemaDeclarations, 0, len(ids))
	for _, id := range ids {
		ordered = append(ordered, files[id])
	}
	full := NewSchemaValidator(ordered)
	if !reflect.DeepEqual(index.GlobalErrors(), full.GlobalErrors()) {
		t.Fatalf("global diagnostics differ\nindexed: %+v\nfull: %+v", index.GlobalErrors(), full.GlobalErrors())
	}
	for _, id := range ids {
		if actual, expected := index.Validate(files[id]), full.Validate(files[id]); !reflect.DeepEqual(actual, expected) {
			t.Fatalf("document %s diagnostics differ\nindexed: %+v\nfull: %+v", id, actual, expected)
		}
	}
}

func TestSchemaIndexEditsMatchFullValidator(t *testing.T) {
	files := map[string]SchemaDeclarations{}
	index := NewSchemaIndex(files)
	type snapshot struct {
		index *SchemaIndex
		files map[string]SchemaDeclarations
	}
	var retained []snapshot
	steps := []struct{ id, tl, tl2 string }{
		{"z", "", "Choice<N:#> = values:[N]int32; Clash#12345678 = x:int32; Size <=> uint32;"},
		{"use", "", "Use = box:Choice<3> n:Size values:[n]int32 fn:call;"},
		{"a", "first#12345678 {T:Type} x:T = Choice T;", "call#11223344 => int32;"},
		{"b", "second#12345678 {n:#} = Choice n;", ""},
		{"a", "// moved\nfirst#12345678 {T:Type} x:T = Choice T;", "call#11223344 => int32;"},
		{"a", "first#87654321 {n:#} = Choice n;", "call#11223344 => int32;"},
		{"b", "", ""},
		{"a", "", ""},
		{"z", "", "Choice<T:Type> = value:T; Size <=> string; call#12345678 => int32; Untagged = value:int32;"},
		{"builtin", "int ? = Int; _ {X:Type} result:X = ReqResult X; zero#00000000 = Zero;", "Int = value:int32;"},
		{"z", "", ""},
		{"builtin", "", ""},
	}
	for _, step := range steps {
		retained = append(retained, snapshot{index, maps.Clone(files)})
		var file *SchemaDeclarations
		if step.tl+step.tl2 != "" {
			parsed := indexTestDeclarations(t, step.id, step.tl, step.tl2)
			files[step.id] = parsed
			file = &parsed
		} else {
			delete(files, step.id)
		}
		index = index.WithDocuments(map[string]*SchemaDeclarations{step.id: file})
		checkSchemaIndex(t, index, files)
	}
	for _, old := range retained {
		checkSchemaIndex(t, old.index, old.files)
	}
}

func TestSchemaIndexBatchUpdatesAndBucketReuse(t *testing.T) {
	stable := indexTestDeclarations(t, "stable", "stable#55667788 = Stable;", "")
	first := indexTestDeclarations(t, "first", "", "Item#12345678 = x:int32;")
	index := NewSchemaIndex(map[string]SchemaDeclarations{"stable": stable, "first": first})
	changed := indexTestDeclarations(t, "first", "", "Other#11223344 = x:int32;")
	duplicate := indexTestDeclarations(t, "second", "", "Other#11223344 = y:int64;")
	next := index.WithDocuments(map[string]*SchemaDeclarations{"first": &changed, "second": &duplicate, "absent": nil})
	checkSchemaIndex(t, next, map[string]SchemaDeclarations{"stable": stable, "first": changed, "second": duplicate})
	if index.documents["stable"] != next.documents["stable"] ||
		index.types[schemaNameShard("Stable")]["Stable"] != next.types[schemaNameShard("Stable")]["Stable"] ||
		index.tags[schemaTagShard(0x55667788)][0x55667788] != next.tags[schemaTagShard(0x55667788)][0x55667788] {
		t.Fatal("unchanged declarations or tags were rebuilt")
	}
	if index.typeByName("Item") == nil || index.typeByName("Other") != nil || next.typeByName("Item") != nil {
		t.Fatal("old snapshot changed or removed declaration remained")
	}
	if next.WithDocuments(nil) != next {
		t.Fatal("empty update replaced the snapshot")
	}
	removed := next.WithDocuments(map[string]*SchemaDeclarations{"first": nil})
	checkSchemaIndex(t, removed, map[string]SchemaDeclarations{"stable": stable, "second": duplicate})
	if removed.typeByName("Other").pr.Begin.FileName() != "/schema/second.tl2" {
		t.Fatal("removing the winning declaration did not promote the next one")
	}
}

func TestSchemaIndexRandomUpdatesMatchFullValidator(t *testing.T) {
	fixtures := []struct{ tl, tl2 string }{
		{"", ""},
		{"item#12345678 {T:Type} value:T = Box T;", ""},
		{"other#12345678 {N:#} values:N*[int] = Box N;", ""},
		{"int ? = Int; _ {T:Type} result:T = ReqResult T;", ""},
		{"zero#00000000 = Zero;", ""},
		{"", "Box<T:Type> = x:T; Size <=> uint32;"},
		{"", "Box<N:#> = values:[N]int32; Size <=> string;"},
		{"", "Box#12345678 => int32;"},
		{"", "Use = x:Box<3> n:Size values:[n]int32 unknown:Missing;"},
		{"", "A <=> B; B <=> A; Use = n:A values:[n]int32;"},
		{"", "Clash#12345678 = x:int32; Clash#12345678 = y:int32;"},
		{"int ? = Int;", "Int#12345678 = x:int32;"},
	}
	pool := make(map[string][]SchemaDeclarations)
	for _, id := range []string{"a", "b", "c", "d", "e"} {
		for _, fixture := range fixtures {
			pool[id] = append(pool[id], indexTestDeclarations(t, id, fixture.tl, fixture.tl2))
		}
	}
	random := rand.New(rand.NewSource(234))
	files := map[string]SchemaDeclarations{}
	index := NewSchemaIndex(files)
	for step := 0; step < 200; step++ {
		changes := map[string]*SchemaDeclarations{}
		for change := 0; change < 1+random.Intn(3); change++ {
			id := string(rune('a' + random.Intn(5)))
			choice := random.Intn(len(fixtures))
			if choice == 0 {
				changes[id] = nil
				delete(files, id)
			} else {
				file := pool[id][choice]
				changes[id] = &file
				files[id] = file
			}
		}
		index = index.WithDocuments(changes)
		checkSchemaIndex(t, index, files)
	}
}

func TestSchemaIndexConcurrentSnapshots(t *testing.T) {
	file := indexTestDeclarations(t, "a", "", "Size <=> uint32; Use = n:Size values:[n]int32;")
	changed := indexTestDeclarations(t, "a", "", "Size <=> string; Use = n:Size values:[n]int32;")
	old := NewSchemaIndex(map[string]SchemaDeclarations{"a": file})
	var workers sync.WaitGroup
	for worker := 0; worker < 4; worker++ {
		workers.Add(1)
		go func() {
			defer workers.Done()
			for i := 0; i < 100; i++ {
				if len(old.Validate(file))+len(old.GlobalErrors()) != 0 {
					t.Error("old snapshot observed new diagnostics")
					return
				}
			}
		}()
	}
	current := old
	for i := 0; i < 20; i++ {
		current = current.WithDocuments(map[string]*SchemaDeclarations{"a": &changed})
		if len(current.Validate(changed)) != 1 {
			t.Fatal("new snapshot missed changed alias category")
		}
	}
	workers.Wait()
}
