// Package daytime converts between epoch seconds and calendar time as
// Inferno's Daytime module does (daytime(2)): the clock is /dev/time and
// the local zone comes from /env/timezone or /locale/timezone, in
// Inferno's timezone file format.
package daytime

import (
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"
)

// Tm is a broken-down time, as Daytime->Tm.
type Tm struct {
	Sec   int    // 0 to 59
	Min   int    // 0 to 59
	Hour  int    // 0 to 23
	Mday  int    // 1 to 31
	Mon   int    // 0 to 11
	Year  int    // year - 1900
	Wday  int    // 0 to 6, Sunday is 0
	Yday  int    // 0 to 365
	Zone  string // zone name
	Tzoff int    // seconds east of GMT
}

var (
	wkday = [...]string{"Sun", "Mon", "Tue", "Wed", "Thu", "Fri", "Sat"}
	month = [...]string{"Jan", "Feb", "Mar", "Apr", "May", "Jun",
		"Jul", "Aug", "Sep", "Oct", "Nov", "Dec"}
)

// Now returns the time in seconds since the epoch, from /dev/time, or 0
// if it cannot be read.
func Now() int {
	b, err := os.ReadFile("/dev/time")
	if err != nil {
		return 0
	}
	us, err := strconv.ParseInt(strings.TrimSpace(string(b)), 10, 64)
	if err != nil {
		return 0
	}
	return int(us / 1000000)
}

// Time returns the current local time as text.
func Time() string { return Text(Local(Now())) }

// Gmt breaks t down in GMT.
func Gmt(t int) *Tm {
	u := time.Unix(int64(t), 0).UTC()
	return &Tm{
		Sec: u.Second(), Min: u.Minute(), Hour: u.Hour(),
		Mday: u.Day(), Mon: int(u.Month()) - 1, Year: u.Year() - 1900,
		Wday: int(u.Weekday()), Yday: u.YearDay() - 1, Zone: "GMT",
	}
}

// zone is a parsed timezone file: standard and daylight names and
// offsets, then pairs of local times bounding daylight time.
type zone struct {
	stname, dlname string
	stdiff, dldiff int
	dlpairs        []int
}

var tz *zone

// Local breaks t down in the local time zone.
func Local(t int) *Tm {
	if tz == nil {
		tz = readZone()
	}
	lt, name, off := t+tz.stdiff, tz.stname, tz.stdiff
	for i := 0; i+1 < len(tz.dlpairs); i += 2 {
		if lt >= tz.dlpairs[i] && lt < tz.dlpairs[i+1] {
			lt, name, off = t+tz.dldiff, tz.dlname, tz.dldiff
			break
		}
	}
	tm := Gmt(lt)
	tm.Zone, tm.Tzoff = name, off
	return tm
}

func readFile(name string) string {
	f, err := os.Open(name)
	if err != nil {
		return ""
	}
	defer f.Close()
	b := make([]byte, 2048)
	n, _ := f.Read(b)
	return string(b[:max(n, 0)])
}

// readZone reads the timezone file; a missing or short one is GMT.
func readZone() *zone {
	z := &zone{stname: "GMT"}
	s := readFile("/env/timezone")
	if s == "" {
		s = readFile("/locale/timezone")
	}
	if s != "" && (s[0] == '/' || s[0] == '#') {
		s = readFile(strings.TrimSuffix(s, "\n"))
	}
	f := strings.FieldsFunc(s, func(r rune) bool { return strings.ContainsRune("\t \n\r", r) })
	if len(f) < 4 {
		return z
	}
	atoi := func(s string) int { n, _ := strconv.Atoi(s); return n }
	z.stname, z.stdiff, z.dlname, z.dldiff = f[0], atoi(f[1]), f[2], atoi(f[3])
	for _, v := range f[4:] {
		z.dlpairs = append(z.dlpairs, atoi(v))
	}
	return z
}

// Text formats tm as Inferno does: "Thu Jan 01 00:00:00 GMT 1970".
func Text(tm *Tm) string {
	return fmt.Sprintf("%s %s %.2d %.2d:%.2d:%.2d %s %d",
		wkday[tm.Wday], month[tm.Mon], tm.Mday, tm.Hour, tm.Min, tm.Sec, tm.Zone, tm.Year+1900)
}

// Filet is a file time as ls -l shows it: month, day and time for a file
// less than six months older than now, otherwise month, day and year.
func Filet(now, file int) string {
	tm := Local(file)
	if now-file < 6*30*24*3600 {
		return fmt.Sprintf("%s %.2d %.2d:%.2d", month[tm.Mon], tm.Mday, tm.Hour, tm.Min)
	}
	return fmt.Sprintf("%s %.2d  %d", month[tm.Mon], tm.Mday, tm.Year+1900)
}
