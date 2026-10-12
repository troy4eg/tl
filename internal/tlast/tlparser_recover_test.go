package tlast

import "testing"

func TestTLRecoveryLegacyReqResultAndBuiltinPlaceholders(t *testing.T) {
	const legacy = "_ {X:Type} result:X = ReqResult X;"
	strict, err := ParseTLFile(legacy, "legacy.tl", LexerOptions{AllowDirty: true})
	if err != nil || len(strict) != 1 {
		t.Fatalf("compiler parser rejected the supported legacy declaration: %v", err)
	}
	text := "---types---\n// legacy result wrapper\n" + legacy + "\n__dict_field {k:Type} {v:Type} key:k value:v = DictField;\nint ? = _;\n"
	ast, errs := ParseTLFileRecovering(text, "legacy.tl")
	if len(errs) != 0 || len(ast) != 3 {
		t.Fatalf("declarations=%+v, errors=%+v", ast, errs)
	}
	if ast[0].Construct.Name.String() != "_" || ast[0].TypeDecl.Name.String() != "ReqResult" || ast[0].Construct.ID != strict[0].Construct.ID {
		t.Fatalf("legacy declaration changed: %+v", ast[0])
	}
	if ast[1].Construct.Name.String() != "__dict_field" || ast[2].TypeDecl.Name.String() != "_" || !ast[2].Builtin {
		t.Fatalf("internal builtins changed: %+v", ast)
	}
	for _, c := range ast {
		pr := c.Construct.NamePR
		if text[pr.Begin.Offset():pr.End.Offset()] != c.Construct.Name.String() {
			t.Fatalf("incorrect constructor name range: %+v", pr)
		}
	}
}

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
