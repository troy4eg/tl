package tlast

import (
	"fmt"
	"reflect"
	"strings"
	"testing"
)

func TestEditingRecoveryBoundaries(t *testing.T) {
	for _, tc := range []struct {
		name, text string
		tl2        bool
		want       []string
		incomplete []bool
	}{
		{"tl2 missing semicolon", "Before = x:int32\n// after\nAfter = y:Before;", true, []string{"Before", "After"}, []bool{true, false}},
		{"tl2 open brackets", "Box<T:Type> = items:List<T,\nAfter = x:int32;", true, []string{"Box", "After"}, []bool{true, false}},
		{"tl2 following function", "Before = x:\nget#12345678 value:int32 => int32;", true, []string{"Before", "get"}, []bool{true, false}},
		{"tl2 valid multiline", "Box<T:Type,\nN:#> = items:[N]T;\nChoice =\n| a n:uint32 items:[n]int32\n| b value:int32;", true, []string{"Box", "Choice"}, []bool{false, false}},
		{"tl1 missing semicolon", "before value:int = Before\n// after\nafter value:Before = After;", false, []string{"before", "after"}, []bool{true, false}},
		{"tl1 open brackets", "box {X:Type} values:[count:# item:(Box X\nafter value:int = After;", false, []string{"box", "after"}, []bool{true, false}},
		{"tl1 sections", "before value:\n---functions---\nget value:int = int;\n---types---\nafter = After;", false, []string{"before", "get", "after"}, []bool{true, false, false}},
		{"tl1 valid anonymous multiline", "box {X:Type}\n vector X\n= Box X;", false, []string{"box"}, []bool{false}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var names []string
			var flags []bool
			var errs []*ParseError
			if tc.tl2 {
				p := ParseTL2FileEditing(tc.text, "test.tl2")
				flags, errs = p.Incomplete, p.Errors
				for _, c := range p.Declarations {
					names = append(names, c.ReferenceName().String())
				}
			} else {
				p := ParseTLFileEditing(tc.text, "test.tl")
				flags, errs = p.Incomplete, p.Errors
				for _, c := range p.Declarations {
					names = append(names, c.Construct.Name.String())
				}
				if tc.name == "tl1 sections" && (!p.Declarations[1].IsFunction || p.Declarations[2].IsFunction) {
					t.Fatal("lost section")
				}
			}
			if !reflect.DeepEqual(names, tc.want) || !reflect.DeepEqual(flags, tc.incomplete) {
				t.Fatalf("names=%v incomplete=%v errors=%v", names, flags, errs)
			}
			bad := false
			for _, b := range flags {
				bad = bad || b
			}
			if bad != (len(errs) > 0) {
				t.Fatalf("syntax diagnostics=%v", errs)
			}
		})
	}
}

func TestEditingRetainsPartialTypesAndParameters(t *testing.T) {
	text := "Box<T:Type, N:#> = n:uint32 items:[N]List<T,"
	p := ParseTL2FileEditing(text, "test.tl2")
	if len(p.Declarations) != 1 || !p.Incomplete[0] {
		t.Fatalf("%+v", p)
	}
	c := p.Declarations[0]
	if len(c.TypeDecl.TemplateArguments) != 2 || len(c.TypeDecl.Type.StructType.ConstructorFields) != 2 {
		t.Fatalf("%+v", c)
	}
	tr := c.TypeDecl.Type.StructType.ConstructorFields[1].Type.BracketType
	if tr == nil || tr.IndexType.Type.SomeType.Name.String() != "N" || tr.ArrayType.SomeType.Name.String() != "List" || tr.ArrayType.SomeType.Arguments[0].Type.SomeType.Name.String() != "T" {
		t.Fatalf("partial type: %+v", tr)
	}
	for _, p := range c.TypeDecl.TemplateArguments {
		if text[p.PRName.Begin.Offset():p.PRName.End.Offset()] != p.Name {
			t.Fatal("parameter range")
		}
	}
	p = ParseTL2FileEditing("Box<T:", "test.tl2")
	param := p.Declarations[0].TypeDecl.TemplateArguments[0]
	if param.PR.End.Offset() != len("Box<T:") {
		t.Fatalf("unfinished parameter: %+v", param.PR)
	}

	tl := ParseTLFileEditing("box {X:Type} flags:# value:flags.0?X items:[n:# item:(Box X", "test.tl")
	if len(tl.Declarations) != 1 || !tl.Incomplete[0] {
		t.Fatalf("%+v", tl)
	}
	b := tl.Declarations[0]
	if len(b.TemplateArguments) != 1 || len(b.Fields) != 3 || b.Fields[1].Mask.MaskName != "flags" {
		t.Fatalf("%+v", b)
	}
	items := b.Fields[2].ScaleRepeat.Rep
	if len(items) != 2 || items[1].FieldType.Type.String() != "Box" || len(items[1].FieldType.Args) != 1 {
		t.Fatalf("nested fields: %+v", items)
	}
}

func TestEditingRecoveryKeepsFollowingDocumentation(t *testing.T) {
	text := "Before = x:int32 // inline\n// following\nAfter = x:int32;"
	p := ParseTL2FileEditing(text, "test.tl2")
	if len(p.Declarations) != 2 || p.Declarations[1].CommentBefore != "// following" {
		t.Fatalf("following documentation: %+v", p)
	}
}

func TestEditingCompleteTL2MatchesStrictAST(t *testing.T) {
	var large strings.Builder
	large.WriteString("Many = ")
	for i := range 80 {
		fmt.Fprintf(&large, "field%d:List<Box<int32,7>,3> ", i)
	}
	large.WriteString(";")
	for _, text := range []string{
		"// 😀 header\r\nBox<T:Type,N:#> = value:T items:[N]List<T,7>;\r\nUse = box:Box<int32,2>;",
		"Choice = | first value:List<int32> | second values:[]string; get#12345678 id:int32 => value:Choice;",
		"Alias <=> Box<string,3>; Empty = ; Ignored = _:byte _hidden:uint32 optional?:int32;",
		large.String(),
	} {
		strict, err := ParseTL2File(text, "parity.tl2", LexerOptions{LexerLanguage: TL2})
		if err != nil {
			t.Fatal(err)
		}
		edited := ParseTL2FileEditing(text, "parity.tl2")
		if len(edited.Errors) != 0 || !reflect.DeepEqual(edited.Declarations, strict.Combinators) {
			t.Fatalf("editing AST/ranges differ from strict parse: errors=%v, text=%q", edited.Errors, text)
		}
		for _, incomplete := range edited.Incomplete {
			if incomplete {
				t.Fatal("complete declaration marked incomplete")
			}
		}
	}
}

func TestEditingLargeTL1FieldsAndSections(t *testing.T) {
	var text strings.Builder
	text.WriteString("---types---\nitem {T:Type} flags:# ")
	for i := range 80 {
		fmt.Fprintf(&text, "field%d:flags.0?Box<T,3> nested%d:3*[n:# value:(Box T)] ", i, i)
	}
	text.WriteString("= Item T;\n---functions---\nget value:Item<int> = Item int;\n---types---\nend = End;")
	p := ParseTLFileEditing(text.String(), "large.tl")
	if len(p.Errors) != 0 || len(p.Declarations) != 3 || len(p.Declarations[0].Fields) != 161 || !p.Declarations[1].IsFunction || p.Declarations[2].IsFunction {
		t.Fatalf("large TL1 parse/sections: %v", p.Errors)
	}
	for i, field := range p.Declarations[0].Fields[1:] {
		if i%2 == 0 && (field.Mask == nil || field.Mask.MaskName != "flags") || i%2 == 1 && (!field.IsRepeated || len(field.ScaleRepeat.Rep) != 2) {
			t.Fatalf("lost nested field or mask at %d", i)
		}
	}
}
