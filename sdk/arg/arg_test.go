package arg

import (
	"reflect"
	"testing"
)

func TestOpts(t *testing.T) {
	a := New([]string{"ls", "-lt", "-n5", "-m", "7", "--", "-x", "f"})
	var got []string
	for c := a.Opt(); c != 0; c = a.Opt() {
		s := string(c)
		if c == 'n' || c == 'm' {
			s += "=" + a.Arg()
		}
		got = append(got, s)
	}
	if want := []string{"l", "t", "n=5", "m=7"}; !reflect.DeepEqual(got, want) {
		t.Errorf("opts %q, want %q", got, want)
	}
	if rest := a.Argv(); !reflect.DeepEqual(rest, []string{"-x", "f"}) {
		t.Errorf("argv %q", rest)
	}
}
