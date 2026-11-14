package daytime

import "testing"

func TestText(t *testing.T) {
	for _, c := range []struct {
		t    int
		want string
	}{
		{0, "Thu Jan 01 00:00:00 GMT 1970"},
		{1000000000, "Sun Sep 09 01:46:40 GMT 2001"},
		{-1, "Wed Dec 31 23:59:59 GMT 1969"},
		{951782400, "Tue Feb 29 00:00:00 GMT 2000"},
	} {
		if got := Text(Gmt(c.t)); got != c.want {
			t.Errorf("Text(Gmt(%d)) = %q, want %q", c.t, got, c.want)
		}
	}
	if tm := Gmt(951782400); tm.Yday != 59 || tm.Wday != 2 {
		t.Errorf("yday %d wday %d", tm.Yday, tm.Wday)
	}
}

func TestLocal(t *testing.T) {
	old := tz
	defer func() { tz = old }()
	// Standard time one hour east; daylight time two hours east for a
	// window starting at local midnight on day one.
	tz = &zone{stname: "CET", stdiff: 3600, dlname: "CEST", dldiff: 7200,
		dlpairs: []int{86400, 2 * 86400}}
	if got := Text(Local(0)); got != "Thu Jan 01 01:00:00 CET 1970" {
		t.Errorf("standard: %q", got)
	}
	if got := Text(Local(86400)); got != "Fri Jan 02 02:00:00 CEST 1970" {
		t.Errorf("daylight: %q", got)
	}
	tz = &zone{stname: "GMT"}
	if got := Filet(1000, 0); got != "Jan 01 00:00" {
		t.Errorf("filet recent: %q", got)
	}
	if got := Filet(400*86400, 0); got != "Jan 01  1970" {
		t.Errorf("filet old: %q", got)
	}
}
