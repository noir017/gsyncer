package ignore

import (
	"reflect"
	"testing"
)

func TestToRsyncFilters(t *testing.T) {
	in := []string{"*.log", "build/", "!important.log"}
	got := ToRsyncFilters(in)
	want := []string{"+ important.log", "- build/", "- *.log"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v, want %v", got, want)
	}
}

func TestSkipsBlankAndComments(t *testing.T) {
	in := []string{"", "  ", "# comment", "*.tmp"}
	got := ToRsyncFilters(in)
	want := []string{"- *.tmp"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v, want %v", got, want)
	}
}

func TestEmpty(t *testing.T) {
	if got := ToRsyncFilters(nil); len(got) != 0 {
		t.Fatalf("got %v", got)
	}
}

// Characterization tests pinning today's verbatim passthrough of the pattern
// body. These lock in the documented behavior so any future decision to
// actually rewrite globs is a deliberate, reviewed change — they are not
// correctness guarantees about rsync's matching.
func TestPassesThroughPatternBody(t *testing.T) {
	cases := []struct {
		name string
		in   []string
		want []string
	}{
		{"leading slash anchors verbatim", []string{"/foo"}, []string{"- /foo"}},
		{"middle slash anchors verbatim", []string{"a/b"}, []string{"- a/b"}},
		{"trailing slash dir-only verbatim", []string{"build/"}, []string{"- build/"}},
		{"double star forwarded verbatim", []string{"**/node_modules", "dist/**"},
			[]string{"- dist/**", "- **/node_modules"}},
		// slash-less floats to every level; slashed is anchored — neither rewritten.
		{"slashless vs slashed", []string{"*.log", "logs/debug.log"},
			[]string{"- logs/debug.log", "- *.log"}},
		// KNOWN GAP: rsync will not re-include keep under the excluded /foo dir,
		// but the ordering (include first) is still emitted correctly.
		{"negation reversed ahead of exclude", []string{"/foo", "!/foo/keep"},
			[]string{"+ /foo/keep", "- /foo"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := ToRsyncFilters(tc.in)
			if !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("got %v, want %v", got, tc.want)
			}
		})
	}
}

func TestTrimsWhitespaceAndNegationSpacing(t *testing.T) {
	// Surrounding whitespace is stripped (documents the escaped-trailing-space gap).
	if got := ToRsyncFilters([]string{"  spaced.txt  "}); !reflect.DeepEqual(got, []string{"- spaced.txt"}) {
		t.Fatalf("whitespace not trimmed: %v", got)
	}
	// "! foo" and "!foo" are equivalent (TrimSpace applied to the remainder).
	if got := ToRsyncFilters([]string{"! foo"}); !reflect.DeepEqual(got, []string{"+ foo"}) {
		t.Fatalf("negation spacing: %v", got)
	}
}
