package tllsp

import (
	"fmt"
	"strings"
	"testing"

	"github.com/VKCOM/tl/internal/tlast"
)

// BenchmarkLSPRegistry isolates registry work from parsing and local semantic
// checks. The existing Change/FileEvent/QuickFix cases measure the full handlers.
func BenchmarkLSPRegistry(b *testing.B) {
	for _, declarations := range []int{1000, 10000} {
		b.Run(fmt.Sprint(declarations), func(b *testing.B) {
			f := newBenchmarkFixture(b, declarations)
			files := make(map[string]tlast.SchemaDeclarations, len(f.base.docs))
			for uri, d := range f.base.docs {
				files[uri] = tlast.SchemaDeclarations{TL: d.tl, TL2: d.tl2}
			}
			b.Run("Build", func(b *testing.B) {
				b.ReportAllocs()
				for b.Loop() {
					benchmarkResult = tlast.NewSchemaIndex(files)
				}
			})
			for _, language := range []string{"tl2", "tl"} {
				file := 0
				before, after := "id:int64", "id:int32"
				if language == "tl" {
					file, before, after = 3, "value:flags.0?int", "value:flags.0?long"
				}
				uri := f.uri(file)
				updated := parseDocument(uri, strings.Replace(f.files[file].text, before, after, 1), 1, true)
				if updated.text == f.files[file].text || len(updated.errors) != 0 {
					b.Fatal("invalid registry edit")
				}
				changed := tlast.SchemaDeclarations{TL: updated.tl, TL2: updated.tl2}
				original := files[uri]
				b.Run("Update/"+language, func(b *testing.B) {
					index := f.base.index.validator
					updates := map[string]*tlast.SchemaDeclarations{uri: &changed}
					checked := index.WithDocuments(updates)
					if len(checked.GlobalErrors())+len(checked.Validate(changed)) != 0 {
						b.Fatal("registry update introduced diagnostics")
					}
					iteration := 0
					b.ReportAllocs()
					for b.Loop() {
						if iteration%2 == 0 {
							updates[uri] = &changed
						} else {
							updates[uri] = &original
						}
						index = index.WithDocuments(updates)
						benchmarkResult = index
						iteration++
					}
					b.ReportMetric(benchmarkDeclarationsPerFile, "declarations/op")
				})
			}
		})
	}
}
