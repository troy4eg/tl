package tlast

import "testing"

func TestParseTL2FileRecovering(t *testing.T) {
	text := "// comment ; is not a boundary\nfirst = x:int32;\nbroken = x:;\nsecond = y:first;\nillegal = z:(int32);\nthird = z:second;\nbadChar = x:$;\nfourth = z:third;\nunfinished = x:"
	ast, errs := ParseTL2FileRecovering(text, "test.tl2")
	if len(ast.Combinators) != 4 || len(errs) != 4 {
		t.Fatalf("got %d declarations, %d errors: %v", len(ast.Combinators), len(errs), errs)
	}
	names := []string{"first", "second", "third", "fourth"}
	for i, comb := range ast.Combinators {
		if comb.TypeDecl.Name.String() != names[i] {
			t.Fatalf("declaration %d: %v", i, comb)
		}
		b, e := comb.TypeDecl.PRName.Begin.Offset(), comb.TypeDecl.PRName.End.Offset()
		if text[b:e] != names[i] {
			t.Fatalf("original source range %d: %q", i, text[b:e])
		}
	}
	if ast, err := ParseTL2(text); err == nil || len(ast.Combinators) != 0 {
		t.Fatal("strict generator parser must still reject malformed files")
	}
}

func FuzzParseTL2FileRecovering(f *testing.F) {
	for _, text := range []string{"", "x = a:int32;", "x = a:; y = b:int32;", "// привет 😀\nx = ;", "/* bad */; x = ;", "@;", "x<T:Type> = a:T;", "x = | a int32 | b;"} {
		f.Add(text)
	}
	f.Fuzz(func(t *testing.T, text string) {
		ast, errs := ParseTL2FileRecovering(text, "fuzz.tl2")
		for _, err := range errs {
			b, e := err.Pos.Begin.Offset(), err.Pos.End.Offset()
			if b < 0 || e < b || e > len(text) {
				t.Fatalf("invalid diagnostic range: %d..%d", b, e)
			}
		}
		for _, c := range ast.Combinators {
			b, e := c.PR.Begin.Offset(), c.PR.End.Offset()
			if b < 0 || e < b || e > len(text) {
				t.Fatalf("invalid AST range: %d..%d", b, e)
			}
		}
	})
}
