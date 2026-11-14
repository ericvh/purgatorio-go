package keyring

import "testing"

// A number whose base64 starts with '+' must survive the round trip:
// strtomp takes only '-' as a sign.
func TestB64RoundTrip(t *testing.T) {
	for n := 0; n < 2000; n++ {
		x := Random(512, 1024)
		s := x.IPtoB64()
		if y := B64toIP(s); y == nil || y.v.Cmp(x.v) != 0 {
			t.Fatalf("%q did not round-trip", s)
		}
	}
	if y := B64toIP("-AQ=="); y == nil || y.IPtoInt() != -1 {
		t.Errorf("-AQ==: %v", y)
	}
}
