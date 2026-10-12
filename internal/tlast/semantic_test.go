package tlast

import (
	"reflect"
	"sort"
	"strings"
	"testing"
)

func checkSchema(t *testing.T, tlText, tl2Text string) []SemanticError {
	t.Helper()
	tl, e1 := ParseTLFileRecovering(tlText, "/schema/legacy.tl")
	tl2, e2 := ParseTL2FileRecovering(tl2Text, "/schema/types.tl2")
	if len(e1)+len(e2) > 0 {
		t.Fatalf("invalid fixture: TL=%v TL2=%v", e1, e2)
	}
	return ValidateSchema(tl, tl2.Combinators)
}

func TestSchemaSemanticChecks(t *testing.T) {
	for _, tt := range []struct {
		name, tl, tl2 string
		codes         []string
	}{
		{"tl2 arity", "", "Box<T:Type> = x:T; Use = a:Box b:Box<int32,string>;", []string{"template-arity", "template-arity"}},
		{"tl1 arity", "box {T:Type} x:T = Box T; use a:Box b:(Box int string) = Use;", "", []string{"template-arity", "template-arity"}},
		{"mixed categories", "box {T:Type} {n:#} values:n*[T] = Box T n;", "Use = a:Box<7,int32>;", []string{"argument-category", "argument-category"}},
		{"tl1 categories", "box {T:Type} {n:#} values:n*[T] = Box T n; use a:(Box 3 int) = Use;", "", []string{"argument-category", "argument-category"}},
		{"nat cannot be type", "", "Box<N:#> = x:N;", []string{"argument-category"}},
		{"local cannot take arguments", "", "Box<T:Type> = x:T<int32>;", []string{"template-arity"}},
		{"global duplicate", "", "Item = x:int32; Item = y:int64;", []string{"duplicate-declaration"}},
		{"constructor duplicate", "item = Item; item = Other;", "", []string{"duplicate-declaration"}},
		{"mixed duplicate", "item = Item;", "Item = x:int32;", []string{"duplicate-declaration"}},
		{"incompatible shared type", "first {T:Type} x:T = Choice T; second {n:#} = Choice n;", "", []string{"template-signature"}},
		{"shared parameter names", "first {T:Type} x:T = Choice T; second {X:Type} x:X = Choice X;", "", []string{"template-signature"}},
		{"result argument order", "item {T:Type} {n:#} = Item n T;", "", []string{"template-signature", "template-signature"}},
		{"result arity", "item {T:Type} = Item;", "", []string{"template-signature"}},
		{"duplicate field", "", "Item = x:int32 x:int64;", []string{"duplicate-local"}},
		{"duplicate template", "", "Item<T:Type,T:Type> = x:T;", []string{"duplicate-local"}},
		{"template field collision", "", "Item<T:Type> = T:int32;", []string{"duplicate-local"}},
		{"duplicate variant", "", "Choice = | first x:int32 | first y:int32;", []string{"duplicate-variant"}},
		{"mask wrong type", "item flags:int x:flags.0?int = Item;", "", []string{"field-dependency"}},
		{"mask forward", "item x:flags.0?int flags:# = Item;", "", []string{"field-dependency"}},
		{"mask missing", "item x:flags.0?int = Item;", "", []string{"field-dependency"}},
		{"mask category", "item {T:Type} x:T.0?int = Item T;", "", []string{"argument-category"}},
		{"mask bit", "item flags:# x:flags.32?int = Item;", "", []string{"mask-bit"}},
		{"size wrong type", "", "Item = n:int32 values:[n]int32;", []string{"field-dependency"}},
		{"nested alias size wrong type", "", "Id<T:Type> <=> T; Wrap<T:Type> <=> Id<Id<T>>; Item = n:Wrap<string> values:[n]int32;", []string{"field-dependency"}},
		{"size forward", "", "Item = values:[n]int32 n:uint32;", []string{"field-dependency"}},
		{"size optional", "", "Item = n?:uint32 values:[n]int32;", []string{"field-dependency"}},
		{"tl1 size wrong type", "item n:uint32 values:n*[int] = Item;", "", []string{"field-dependency"}},
		{"implicit size wrong type", "item n:int values:[int] = Item;", "", []string{"field-dependency"}},
		{"function is not type", "", "call#12345678 => int32; Item = x:call;", []string{"function-reference"}},
		{"unknown dependency", "", "Box<T:Type> = x:T; Item = x:Box<Unknown>;", []string{"unknown-type"}},
		{"tl2 legacy primitive", "", "Item = x:int;", []string{"unknown-type"}},
		{"legacy anonymous restriction", "_ {T:Type} x:T = Other T;", "", []string{"legacy-constructor"}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			errs := checkSchema(t, tt.tl, tt.tl2)
			var codes []string
			for _, e := range errs {
				codes = append(codes, e.Code)
				if e.Pos.Begin.FileName() == "" {
					t.Fatalf("error without source: %+v", e)
				}
			}
			sort.Strings(codes)
			sort.Strings(tt.codes)
			if !reflect.DeepEqual(codes, tt.codes) {
				t.Fatalf("codes %v, want %v: %+v", codes, tt.codes, errs)
			}
		})
	}
}

func TestValidSchemaSemantics(t *testing.T) {
	for _, tt := range []struct{ name, tl, tl2 string }{
		{"legacy wrapper", "_ {X:Type} result:X = ReqResult X;", ""},
		{"shared type and builtins", "int ? = Int; first {T:Type} x:T = Choice T; second {T:Type} x:T = Choice T; true = True;", "Use = x:Choice<int32>;"},
		{"templates masks and repeats", "item {T:Type} {n:#} flags:# value:flags.31?T values:n*[T] = Item T n;", ""},
		{"implicit repeat", "vector {T:Type} # [T] = Vector T;", ""},
		{"nested field scope", "item n:# values:n*[n:# more:n*[int]] = Item;", ""},
		{"size aliases", "", "Id<T:Type> <=> T; Size <=> Id<uint32>; Item = n:Size values:[n]int32;"},
		{"nested size aliases", "", "Id<T:Type> <=> T; Wrap<T:Type> <=> Id<Id<T>>; Item = n:Wrap<uint32> values:[n]int32;"},
		{"union scopes", "", "Choice = | first n:uint32 values:[n]int32 | second n:uint32 values:[n]string;"},
		{"dictionary", "", "Item<K:Type> = values:[K]int32;"},
		{"alias unknown", "", "Size <=> External; Item = n:Size values:[n]int32;"},
		{"field same name as type", "", "item = x:int32; Use = item:item;"},
		{"cyclic alias terminates", "", "A <=> B; B <=> A; Item = n:A values:[n]int32;"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			for _, e := range checkSchema(t, tt.tl, tt.tl2) {
				if e.Code != "unknown-type" {
					t.Fatalf("valid schema: %+v", e)
				}
			}
		})
	}
}

func TestConstructorTagsAcrossFormats(t *testing.T) {
	tlText := "a#12345678 = A; zero#00000000 = Zero;"
	tl2Text := "B#12345678 = x:int32; call#12345678 => int32; Untagged = x:int32;"
	err := checkSchema(t, tlText, tl2Text)
	if len(err) != 3 || err[0].Code != "crc-zero" || err[1].Code != "crc-conflict" || err[2].Code != "crc-conflict" {
		t.Fatalf("tags: %+v", err)
	}
	if err[1].Pos.Begin.Offset() != strings.Index(tl2Text, "#12345678") || err[1].Related == nil || err[1].Related.Begin.FileName() != "/schema/legacy.tl" {
		t.Fatalf("tag locations: %+v", err[1])
	}
	// Repeated identical declarations also collide on implicitly calculated CRC.
	err = checkSchema(t, "a = A; a = A;", "")
	if len(err) != 2 || err[1].Code != "crc-conflict" {
		t.Fatalf("implicit CRC: %+v", err)
	}
}
