package tllsp

import "testing"

func TestLineEndingPositions(t *testing.T) {
	s := newSource("a\r\nb\rc\n")
	for _, tc := range []struct {
		offset   int
		position position
	}{{1, position{0, 1}}, {2, position{0, 1}}, {3, position{1, 0}}, {4, position{1, 1}}, {5, position{2, 0}}, {7, position{3, 0}}} {
		if got := s.position(tc.offset); got != tc.position {
			t.Errorf("offset %d: %+v", tc.offset, got)
		}
		if _, err := s.offset(tc.position); err != nil {
			t.Fatal(err)
		}
	}
	for _, text := range []string{"a\r", "a\n", "a\r\n", "// 😀\r\n"} {
		s := newSource(text)
		p := s.position(len(text))
		offset, err := s.offset(p)
		if err != nil || offset != len(text) {
			t.Fatalf("EOF %q: %+v, %d, %v", text, p, offset, err)
		}
	}
}
