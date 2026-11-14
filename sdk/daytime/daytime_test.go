package daytime

import "testing"

func TestGmt(t *testing.T) {
	cases := []struct {
		tim  int
		text string
	}{
		{0, "Thu Jan 01 00:00:00 GMT 1970"},
		{951782400, "Tue Feb 29 00:00:00 GMT 2000"},
		{1234567890, "Fri Feb 13 23:31:30 GMT 2009"},
		{-1, "Wed Dec 31 23:59:59 GMT 1969"},
		{1798761599, "Thu Dec 31 23:59:59 GMT 2026"},
	}
	for _, c := range cases {
		tm := Gmt(c.tim)
		if got := Text(tm); got != c.text {
			t.Errorf("Gmt(%d) = %q, want %q", c.tim, got, c.text)
		}
		if back := Tm2epoch(tm); back != c.tim {
			t.Errorf("Tm2epoch(Gmt(%d)) = %d", c.tim, back)
		}
	}
}

func TestLocalIn(t *testing.T) {
	tz := ParseTimezone("EST -18000 EDT -14400\n 9943200 25664400\n", nil)
	if tz == nil || tz.Stname != "EST" || tz.Dldiff != -14400 || len(tz.Dlpairs) != 2 {
		t.Fatalf("ParseTimezone = %+v", tz)
	}
	if got := Text(LocalIn(tz, 0)); got != "Wed Dec 31 19:00:00 EST 1969" {
		t.Errorf("standard: %q", got)
	}
	if got := Text(LocalIn(tz, 10000000)); got != "Sun Apr 26 13:46:40 EDT 1970" {
		t.Errorf("daylight: %q", got)
	}
	if ParseTimezone("GMT 0", nil) != nil {
		t.Error("short zone parsed")
	}
}

func TestFilet(t *testing.T) {
	SetTimezone(&Timezone{Stname: "GMT"})
	defer SetTimezone(nil)
	now := 1234567890
	if got := Filet(now, now-3600); got != "Feb 13 22:31" {
		t.Errorf("recent: %q", got)
	}
	if got := Filet(now, 0); got != "Jan 01  1970" {
		t.Errorf("old: %q", got)
	}
}

func TestString2tm(t *testing.T) {
	cases := []struct {
		in   string
		want int
	}{
		{"Sun, 06 Nov 1994 08:49:37 GMT", 784111777},
		{"Sunday, 06-Nov-94 08:49:37 GMT", 784111777},
		{"Sun Nov  6 08:49:37 1994", 784111777},
		{"Sun, 06 Nov 1994 08:49:37 +0100", 784111777 - 3600},
		{"Fri Feb 13 23:31:30 GMT 2009", 1234567890},
	}
	for _, c := range cases {
		tm := String2tm(c.in)
		if tm == nil {
			t.Errorf("String2tm(%q) = nil", c.in)
			continue
		}
		if got := Tm2epoch(tm); got != c.want {
			t.Errorf("String2tm(%q) = %d, want %d", c.in, got, c.want)
		}
	}
	for _, bad := range []string{"", "Xyz Nov 6", "Sun Nov 40 00:00:00 1994"} {
		if String2tm(bad) != nil {
			t.Errorf("String2tm(%q) parsed", bad)
		}
	}
}
