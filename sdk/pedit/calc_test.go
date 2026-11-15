package pedit

import "testing"

func TestParseexpr(t *testing.T) {
	// dot 1000, dollar 1000000, size 1000000, as calc.y's test driver.
	cases := []struct {
		in, err string
		v       int64
	}{
		{"12", "", 12},
		{".", "", 1000},
		{"$", "", 1000000},
		{".+50%", "", 501000},
		{"1+2*3", "", 7},
		{"(1+2)*3", "", 9},
		{"10-4-3", "", 3},
		{"-5+10", "", 5},
		{"-5%", "", -50000},
		{"100/7", "", 14},
		{" 3 * ( 2 + 1 ) ", "", 9},
		{"1/0", "division by zero", 0},
		{"", "syntax error", 0},
		{"1+", "syntax error", 0},
		{"(1", "syntax error", 0},
		{"x", "syntax error", 0},
		{"1 2", "syntax error", 0},
	}
	for _, c := range cases {
		v, err := Parseexpr(c.in, 1000, 1000000, 1000000)
		if v != c.v || err != c.err {
			t.Errorf("%q: got %d %q, want %d %q", c.in, v, err, c.v, c.err)
		}
	}
}
