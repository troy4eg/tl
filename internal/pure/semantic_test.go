package pure

import (
	"errors"
	"io"
	"strings"
	"testing"

	"github.com/VKCOM/tl/internal/tlast"
)

func TestSharedEditorAndCompilerChecks(t *testing.T) {
	for _, tt := range []struct{ name, tl, tl2, code, compilerMessage string }{
		{"arity", "", "Box<T:Type> = value:T; Item = x:Box;", "template-arity", "must have 1 template arguments, has 0"},
		{"cross-format tags", "item#12345678 = Item;", "Other#12345678 = x:int32;", "crc-conflict", "constructor tag #12345678 is used again"},
		{"zero tag", "item#00000000 = Item;", "", "crc-zero", "constructor tag 0 is prohibited"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			tl, err := tlast.ParseTLFile(tt.tl, "/test/legacy.tl", tlast.LexerOptions{AllowDirty: true})
			if err != nil {
				t.Fatal(err)
			}
			tl2, err := tlast.ParseTL2File(tt.tl2, "/test/types.tl2", tlast.LexerOptions{LexerLanguage: tlast.TL2})
			if err != nil {
				t.Fatal(err)
			}
			editorErrors := tlast.ValidateSchema(tl, tl2.Combinators)
			if len(editorErrors) != 1 || editorErrors[0].Code != tt.code {
				t.Fatalf("editor checks: %+v", editorErrors)
			}
			k := NewKernel(&OptionsKernel{ErrorWriter: io.Discard})
			k.filesTL1, k.filesTL2 = tl, tl2.Combinators
			err = k.Compile()
			if err == nil || !strings.Contains(err.Error(), tt.compilerMessage) {
				t.Fatalf("compiler disagrees: %v", err)
			}
			var pe *tlast.ParseError
			if !errors.As(err, &pe) || pe.Pos.Begin.FileName() != editorErrors[0].Pos.Begin.FileName() || pe.Pos.Begin.Offset() != editorErrors[0].Pos.Begin.Offset() {
				t.Fatalf("compiler/editor source mismatch: %v / %+v", err, editorErrors[0])
			}
		})
	}
}
