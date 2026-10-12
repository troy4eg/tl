package tlast

import "testing"

func TestTLRecoveryPreservesSectionsAndPositions(t *testing.T) {
	text := "---types---\n// docs\nitem id:int = Item;\nbad value: = Bad;\n---functions---\nget value:Item = Item;\n---types---\nother = Other;"
	ast, errs := ParseTLFileRecovering(text, "schema.tl")
	if len(ast) != 3 || len(errs) != 1 || !ast[1].IsFunction || ast[2].IsFunction {
		t.Fatalf("declarations=%+v, errors=%+v", ast, errs)
	}
	if ast[0].CommentBefore != "// docs" {
		t.Errorf("comment: %q", ast[0].CommentBefore)
	}
	for _, c := range ast {
		pr := c.Construct.NamePR
		if text[pr.Begin.Offset():pr.End.Offset()] != c.Construct.Name.String() {
			t.Fatalf("incorrect name range: %+v", pr)
		}
	}
}

func FuzzTLRecovery(f *testing.F) {
	f.Add("item flags:# value:flags.0?int = Item; ---functions--- get = Item;")
	f.Add("broken @; good = Good;")
	f.Fuzz(func(t *testing.T, text string) {
		ast, _ := ParseTLFileRecovering(text, "fuzz.tl")
		for _, c := range ast {
			if c.PR.Begin.Offset() < 0 || c.PR.End.Offset() > len(text) {
				t.Fatal("range outside source")
			}
		}
	})
}
