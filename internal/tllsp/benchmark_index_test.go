package tllsp

import (
	"fmt"
	"strings"
	"testing"
)

// Isolate index maintenance from parsing and protocol/diagnostic publication.
// Both sizes replace the same already-parsed 100-declaration file.
func BenchmarkLSPIndexEdit(b *testing.B) {
	for _, declarations := range []int{1000, 10000} {
		b.Run(fmt.Sprint(declarations), func(b *testing.B) {
			f := newBenchmarkFixture(b, declarations)
			s, _ := f.server()
			uri, text := f.uri(0), f.files[0].text
			versions := []*document{
				parseDocument(uri, text, 1, true),
				parseDocument(uri, strings.Replace(text, "id:int32", "id:int64", 1), 2, true),
			}
			if versions[0].text == versions[1].text {
				b.Fatal("missing field edit")
			}
			iteration := 0
			b.ReportAllocs()
			for b.Loop() {
				s.setDocument(uri, versions[iteration%2])
				s.refreshIndex(false)
				iteration++
			}
			defs := s.definitions("bench.Record000004")
			if len(defs) != 1 || defs[0].doc != versions[(iteration-1)%2] {
				b.Fatal("index did not replace the changed document")
			}
		})
	}
}
