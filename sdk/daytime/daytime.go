// Package daytime converts times as Inferno's Daytime module does
// (appl/lib/daytime.b, daytime(2)): seconds since the epoch to broken-down
// local or GMT time, and the text forms date(1) and ls(1) print. The time
// zone comes from /env/timezone or /locale/timezone in Inferno's format;
// with neither, local time is GMT.
package daytime

import (
	"fmt"
	"os"
	"strconv"
	"strings"
)

// Tm is a broken-down time, as Daytime->Tm.
type Tm struct {
	Sec   int    // 0 to 59
	Min   int    // 0 to 59
	Hour  int    // 0 to 23
	Mday  int    // 1 to 31
	Mon   int    // 0 to 11
	Year  int    // year-1900
	Wday  int    // 0 to 6, Sunday is 0
	Yday  int    // 0 to 365
	Zone  string // time zone name
	Tzoff int    // seconds east of GMT
}

var (
	dmsize  = [12]int{31, 28, 31, 30, 31, 30, 31, 31, 30, 31, 30, 31}
	ldmsize = [12]int{31, 29, 31, 30, 31, 30, 31, 31, 30, 31, 30, 31}
	wkday   = []string{"Sun", "Mon", "Tue", "Wed", "Thu", "Fri", "Sat"}
	weekday = []string{"Sunday", "Monday", "Tuesday", "Wednesday", "Thursday", "Friday", "Saturday"}
	month   = []string{"Jan", "Feb", "Mar", "Apr", "May", "Jun", "Jul", "Aug", "Sep", "Oct", "Nov", "Dec"}
)

// Timezone is a parsed Inferno time zone file: standard and daylight
// names and offsets, and pairs of epoch times between which daylight time
// applies.
type Timezone struct {
	Stname, Dlname string
	Stdiff, Dldiff int
	Dlpairs        []int
}

var zone *Timezone

// Now returns the time in seconds since the epoch, from /dev/time.
func Now() int {
	b, err := readfile("/dev/time")
	if err != nil {
		return 0
	}
	us, _ := strconv.ParseInt(strings.TrimSpace(b), 10, 64)
	return int(us / 1000000)
}

// Time returns the current local time as Text prints it.
func Time() string { return Text(Local(Now())) }

// SetTimezone replaces the zone Local uses; nil rereads it.
func SetTimezone(tz *Timezone) { zone = tz }

// Local breaks tim down in the local time zone.
func Local(tim int) *Tm {
	if zone == nil {
		zone = ReadTimezone("")
	}
	return LocalIn(zone, tim)
}

// LocalIn breaks tim down in time zone tz.
func LocalIn(tz *Timezone, tim int) *Tm {
	t := tim + tz.Stdiff
	dl := false
	for i := 0; i+1 < len(tz.Dlpairs); i += 2 {
		if t >= tz.Dlpairs[i] && t < tz.Dlpairs[i+1] {
			t = tim + tz.Dldiff
			dl = true
			break
		}
	}
	ct := Gmt(t)
	if dl {
		ct.Zone, ct.Tzoff = tz.Dlname, tz.Dldiff
	} else {
		ct.Zone, ct.Tzoff = tz.Stname, tz.Stdiff
	}
	return ct
}

// Gmt breaks tim down as GMT.
func Gmt(tim int) *Tm {
	t := &Tm{}
	hms := tim % 86400
	day := tim / 86400
	if hms < 0 {
		hms += 86400
		day--
	}
	t.Sec = hms % 60
	d1 := hms / 60
	t.Min = d1 % 60
	t.Hour = d1 / 60
	t.Wday = (day + 7340036) % 7
	if day >= 0 {
		for d1 = 70; day >= Dysize(d1+1900); d1++ {
			day -= Dysize(d1 + 1900)
		}
	} else {
		for d1 = 70; day < 0; d1-- {
			day += Dysize(d1 + 1900 - 1)
		}
	}
	t.Year = d1
	t.Yday = day
	dm := &dmsize
	if Dysize(d1+1900) == 366 {
		dm = &ldmsize
	}
	for d1 = 0; day >= dm[d1]; d1++ {
		day -= dm[d1]
	}
	t.Mday = day + 1
	t.Mon = d1
	t.Zone = "GMT"
	return t
}

// Text formats t as "Thu Jan 01 00:00:00 GMT 1970".
func Text(t *Tm) string {
	return fmt.Sprintf("%s %s %.2d %.2d:%.2d:%.2d %s %d",
		wkday[t.Wday], month[t.Mon], t.Mday, t.Hour, t.Min, t.Sec, t.Zone, 1900+t.Year)
}

// Filet formats a file's time as ls -l does: "Jan 02 15:04" when the file
// is less than six months older than now, else "Jan 02  2006".
func Filet(now, file int) string {
	t := Local(file)
	if now-file < 6*30*24*3600 {
		return fmt.Sprintf("%s %.2d %.2d:%.2d", month[t.Mon], t.Mday, t.Hour, t.Min)
	}
	return fmt.Sprintf("%s %.2d  %d", month[t.Mon], t.Mday, 1900+t.Year)
}

// Dysize is the number of days in year y.
func Dysize(y int) int {
	if y%4 == 0 && (y%100 != 0 || y%400 == 0) {
		return 366
	}
	return 365
}

// ReadTimezone reads an Inferno time zone file: fname under /locale, or
// /env/timezone then /locale/timezone when fname is empty. A file whose
// content is a path names the real file. Missing or short files give GMT.
func ReadTimezone(fname string) *Timezone {
	tz := &Timezone{Stname: "GMT"}
	var s string
	var err error
	if fname == "" {
		s, err = readfile("/env/timezone")
		if err != nil || s == "" {
			s, err = readfile("/locale/timezone")
		}
	} else {
		if fname[0] != '/' && fname[0] != '#' {
			fname = "/locale/" + fname
		}
		s, err = readfile(fname)
	}
	if err != nil || s == "" {
		return tz
	}
	if s[0] == '/' || s[0] == '#' {
		s, err = readfile(strings.TrimSuffix(s, "\n"))
		if err != nil || s == "" {
			return tz
		}
	}
	return ParseTimezone(s, tz)
}

// ParseTimezone parses the text of a time zone file; def is returned when
// it has fewer than four fields.
func ParseTimezone(s string, def *Timezone) *Timezone {
	f := strings.FieldsFunc(s, func(r rune) bool { return strings.ContainsRune("\t \n\r", r) })
	if len(f) < 4 {
		return def
	}
	tz := &Timezone{Stname: f[0], Stdiff: atoi(f[1]), Dlname: f[2], Dldiff: atoi(f[3])}
	tz.Dlpairs = make([]int, len(f)-4)
	for i, v := range f[4:] {
		tz.Dlpairs[i] = atoi(v)
	}
	return tz
}

// atoi is Limbo's string-to-int conversion: a leading number, else 0.
func atoi(s string) int {
	s = strings.TrimLeft(s, " \t")
	i := 0
	if i < len(s) && (s[i] == '-' || s[i] == '+') {
		i++
	}
	for i < len(s) && s[i] >= '0' && s[i] <= '9' {
		i++
	}
	n, _ := strconv.Atoi(s[:i])
	return n
}

func readfile(name string) (string, error) {
	f, err := os.Open(name)
	if err != nil {
		return "", err
	}
	defer f.Close()
	buf := make([]byte, 2048)
	n, err := f.Read(buf)
	if n <= 0 {
		return "", err
	}
	return string(buf[:n]), nil
}

// Tm2epoch converts a broken-down time back to seconds since the epoch.
func Tm2epoch(tm *Tm) int {
	secs := 0
	yr := tm.Year + 1900
	if yr < 1970 {
		for i := yr; i < 1970; i++ {
			secs -= Dysize(i) * 86400
		}
	} else {
		for i := 1970; i < yr; i++ {
			secs += Dysize(i) * 86400
		}
	}
	dm := &dmsize
	if Dysize(yr) == 366 {
		dm = &ldmsize
	}
	for i := 0; i < tm.Mon; i++ {
		secs += dm[i] * 86400
	}
	secs += (tm.Mday - 1) * 86400
	secs += tm.Hour*3600 + tm.Min*60 + tm.Sec
	return secs - tm.Tzoff
}

// String2tm parses the date forms Daytime accepts:
//
//	Sun, 06 Nov 1994 08:49:37 TZ   (RFC 822, RFC 1123)
//	Sunday, 06-Nov-94 08:49:37 TZ  (RFC 850)
//	Sun Nov  6 08:49:37 1994       (asctime, GMT unless a zone is given)
//
// It returns nil when date does not parse.
func String2tm(date string) *Tm {
	tm := &Tm{}
	var w string
	date, w = dateword(date)
	if tm.Wday = lookup(wkday, w); tm.Wday < 0 {
		if tm.Wday = lookup(weekday, w); tm.Wday < 0 {
			return nil
		}
	}
	odate := date
	date, w = dateword(date)
	if tm.Mon = lookup(month, w); tm.Mon >= 0 {
		date, tm.Mday = datenum(date)
		if tm.Mday < 1 || tm.Mday > 31 {
			return nil
		}
		var ok bool
		if date, ok = hhmmss(date, tm); !ok {
			return nil
		}
		date = strings.TrimLeft(date, " ")
		if date != "" && !(date[0] >= '0' && date[0] <= '9') {
			if i := strings.IndexByte(date, ' '); i >= 0 {
				tm.Zone, tm.Tzoff = tzinfo(date[:i])
				date = date[i:]
			}
		}
		_, tm.Year = datenum(date)
		if tm.Year > 1900 {
			tm.Year -= 1900
		}
		if tm.Zone == "" {
			tm.Zone, tm.Tzoff = "GMT", 0
		}
		return tm
	}
	date = odate
	date, tm.Mday = datenum(date)
	if tm.Mday < 1 || tm.Mday > 31 {
		return nil
	}
	date, w = dateword(date)
	if tm.Mon = lookup(month, w); tm.Mon < 0 {
		return nil
	}
	date, tm.Year = datenum(date)
	if tm.Year > 1900 {
		tm.Year -= 1900
	}
	rest, ok := hhmmss(date, tm)
	if !ok {
		return nil
	}
	if tm.Zone, tm.Tzoff = tzinfo(rest); tm.Zone == "" {
		return nil
	}
	return tm
}

func isalnum(c byte) bool {
	return c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9'
}

func isdigit(c byte) bool { return c >= '0' && c <= '9' }

func dateword(date string) (string, string) {
	i := 0
	for i < len(date) && !isalnum(date[i]) {
		i++
	}
	date = date[i:]
	j := 0
	for j < len(date) && isalnum(date[j]) {
		j++
	}
	return date[j:], date[:j]
}

func datenum(date string) (string, int) {
	i := 0
	for i < len(date) && !isdigit(date[i]) {
		i++
	}
	date = date[i:]
	j := 0
	for j < len(date) && isdigit(date[j]) {
		j++
	}
	n, _ := strconv.Atoi(date[:j])
	return date[j:], n
}

func lookup(a []string, s string) int {
	for i, x := range a {
		if x == s {
			return i
		}
	}
	return -1
}

func hhmmss(date string, tm *Tm) (string, bool) {
	date, tm.Hour = datenum(date)
	if tm.Hour < 0 || tm.Hour >= 24 {
		return "", false
	}
	date, tm.Min = datenum(date)
	if tm.Min < 0 || tm.Min >= 60 {
		return "", false
	}
	date, tm.Sec = datenum(date)
	if tm.Sec < 0 || tm.Sec >= 60 {
		return "", false
	}
	return date, true
}

func tzinfo(tz string) (string, int) {
	tz = strings.Trim(tz, " \t")
	switch tz {
	case "":
		return "GMT", 0
	case "GMT", "UT", "UTC", "Z":
		return "GMT", 0
	}
	if len(tz) == 5 && (tz[0] == '+' || tz[0] == '-') {
		h, m := atoi(tz[1:3]), atoi(tz[3:5])
		if h > 23 || m > 59 {
			return "", 0
		}
		off := h*3600 + m*60
		if tz[0] == '-' {
			off = -off
		}
		return "GMT", off
	}
	var fname string
	switch tz {
	case "CST", "CDT":
		fname = "CST.CDT"
	case "EST", "EDT":
		fname = "EST.EDT"
	case "MST", "MDT":
		fname = "MST.MDT"
	case "PST", "PDT":
		fname = "PST.PDT"
	}
	z := ReadTimezone(fname)
	if z.Stname == tz {
		return z.Stname, z.Stdiff
	}
	if z.Dlname == tz {
		return z.Dlname, z.Dldiff
	}
	return "", 0
}
