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
