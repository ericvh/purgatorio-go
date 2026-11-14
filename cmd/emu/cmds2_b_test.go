package main

import (
	"os"
	"path/filepath"
	"testing"
)

// Scratch files for calc, units and tcs live under root/tmp/cmds2b, seen
// as /tmp/cmds2b in the emu.
func bScratch(t *testing.T, files map[string]string) {
	t.Helper()
	dir := filepath.Join(root, "tmp", "cmds2b")
	if err := os.RemoveAll(dir); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	for name, data := range files {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(data), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

type bCase struct {
	name, stdin string
	args        []string
	out, status string
}

func bRun(t *testing.T, cases []bCase) {
	t.Helper()
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			out, st := emu(t, c.stdin, c.args...)
			if out != c.out || st != c.status {
				t.Errorf("got %q status %q, want %q status %q", out, st, c.out, c.status)
			}
		})
	}
}

func TestCmds2BUnicode(t *testing.T) {
	const usage = "usage: unicode { [-t] hex hex ... | hexmin-hexmax ... | [-n] char ... }\n"
	bRun(t, []bCase{
		{"char", "", []string{"unicode", "pé"}, "0070\n00e9\n", ""},
		{"-n", "", []string{"unicode", "-n", "41"}, "0034\n0031\n", ""},
		{"hex", "", []string{"unicode", "41", "3b1"}, "A\nα\n", ""},
		{"-t", "", []string{"unicode", "-t", "41", "42"}, "AB", ""},
		{"range", "", []string{"unicode", "41-4a"},
			"0041 A\t0042 B\t0043 C\t0044 D\t0045 E\t0046 F\t0047 G\t0048 H\n0049 I\t004a J\n", ""},
		{"bad range", "", []string{"unicode", "41-3"}, "unicode: bad range 41-3\n", ""},
		{"bad value", "", []string{"unicode", "110000"}, "unicode: bad unicode value 110000\n", ""},
		{"usage", "", []string{"unicode"}, usage, ""},
	})
}

func TestCmds2BTcs(t *testing.T) {
	bScratch(t, map[string]string{
		"l1":    "caf\xe9\n",
		"l9":    "\xa4\n",
		"bad":   "x\n",
		"cs":    "x=\n\tdesc=X\n\tstob=nosuch\n",
		"quote": "x='\n",
		"u16":   "\xff\xfeh\x00i\x00",
	})
	const all = "utf-8 utf-16 utf-16be utf-16le iso-8859-1 iso-8859-15\n"
	bRun(t, []bCase{
		{"copy", "héllo\n", []string{"tcs"}, "héllo\n", ""},
		{"-l", "", []string{"tcs", "-l"}, all, ""},
		{"-lv", "", []string{"tcs", "-lv"}, "utf-8\tUnicode UTF-8\nutf-16\tUnicode UTF-16\n" +
			"utf-16be\tUnicode UTF-16 big endian\nutf-16le\tUnicode UTF-16 little endian\n" +
			"iso-8859-1\tLatin-1\niso-8859-15\tLatin-9\n", ""},
		{"aliases", "", []string{"tcs", "-l", "LATIN1"}, "iso-8859-1 latin1 l1 iso8859-1 iso_8859-1\n", ""},
		{"aliases -v", "", []string{"tcs", "-lv", "ascii"}, "Unicode UTF-8\nutf-8 utf8 ascii us-ascii\n", ""},
		{"no aliases", "", []string{"tcs", "-l", "ebcdic"}, "unknown charset ebcdic\n", ""},
		{"from latin1", "", []string{"tcs", "-f", "latin1", "/tmp/cmds2b/l1"}, "café\n", ""},
		{"bad alias", "", []string{"tcs", "-f", "8859-1", "/tmp/cmds2b/l1"},
			"tcs: 8859-1: unknown charset 8859-1\n", "fail:badarg"},
		{"latin9", "", []string{"tcs", "-f", "l9", "/tmp/cmds2b/l9", "/tmp/cmds2b/l1"}, "€\ncafé\n", ""},
		{"to latin1", "é€\n", []string{"tcs", "-t", "latin1"}, "\xe9?\n", ""},
		{"to utf16", "hi", []string{"tcs", "-t", "utf16"}, "\xfe\xff\x00h\x00i", ""},
		{"to utf16le", "hi", []string{"tcs", "-t", "utf16le"}, "h\x00i\x00", ""},
		{"from utf16", "", []string{"tcs", "-f", "utf16", "/tmp/cmds2b/u16"}, "hi", ""},
		{"bad ocs", "", []string{"tcs", "-t", "Klingon"}, "tcs: Klingon: unknown charset klingon\n", "fail:badarg"},
		{"no file", "", []string{"tcs", "/tmp/cmds2b/none"},
			"tcs: cannot open /tmp/cmds2b/none: file does not exist\n", "fail:open"},
		{"no config", "", []string{"tcs", "-C", "/tmp/cmds2b/none", "-l"},
			"convcs: bad path: file does not exist\n", "fail:init"},
		{"bad config", "", []string{"tcs", "-C", "/tmp/cmds2b/quote", "-l"},
			"convcs: /tmp/cmds2b/quote:1: missing quote\n", "fail:init"},
		{"no converter", "", []string{"tcs", "-C", "/tmp/cmds2b/cs", "-f", "x"},
			"tcs: utf8: unknown charset utf8\n", "fail:badarg"},
		{"unknown converter", "", []string{"tcs", "-C", "/tmp/cmds2b/cs", "-t", "x", "-f", "x"},
			"tcs: x: cannot load converter: nosuch: unknown converter\n", "fail:badarg"},
		{"usage", "", []string{"tcs", "-q"},
			"usage: tcs [-C configfile] [-l] [-f ics] [-t ocs] file ...\n", "fail:usage"},
	})
}

func TestCmds2BUnits(t *testing.T) {
	bScratch(t, map[string]string{
		"db":  "m\t#\nsec\t#\nfoot\t.3048 m\nfoot\t2 m\nmph\tmile/hour\n",
		"bad": "m\t#\nx\t(\nx\t(\nx\t(\nx\t(\nx\t(\nx\t(\n",
	})
	bRun(t, []bCase{
		{"inch", "inch\ncm\n", []string{"units"},
			"you have: you want: \t* 2.54\n\t/ 0.3937008\nyou have: \n", ""},
		{"man example", "15 pounds force/in²\natm\n", []string{"units"},
			"you have: you want: \t* 1.020689\n\t/ 0.9797299\nyou have: \n", ""},
		{"celsius", "100 °C\n°F\n", []string{"units"}, "you have: you want: \tis 212 °F\nyou have: \n", ""},
		{"prefix", "2 kilometers\nmiles\n", []string{"units"},
			"you have: you want: \t* 1.242742\n\t/ 0.804672\nyou have: \n", ""},
		{"dims", "km\nlb\n", []string{"units"},
			"you have: you want: \t* 2204.623 m / kg\n\t/ 0.0004535924 kg / m\nyou have: \n", ""},
		{"undefined", "frob\n", []string{"units"}, "you have: 0: frob\n\tundefined frob\nyou have: \n", ""},
		{"syntax", "(m\n", []string{"units"},
			"you have: 0: (m\n\tsyntax error, last name: m\nyou have: \n", ""},
		{"like units", "m+sec\n", []string{"units"},
			"you have: 0: m+sec\n\tadd must be like units\n0: m+sec\n\tadd must be like units\nyou have: \n", ""},
		{"file", "", []string{"units", "-v", "/tmp/cmds2b/db"},
			"m\t#\nsec\t#\nfoot\t0.3048 m\n4: foot\t2 m\n\tredefinition of foot\n" +
				"5: mph\tmile/hour\n\tundefined mile\n5: mph\tmile/hour\n\tundefined hour\nmph\t1\n" +
				"you have: \n", ""},
		{"errors", "", []string{"units", "/tmp/cmds2b/bad"},
			"2: x\t(\n\tsyntax error, last name: x\n3: x\t(\n\tsyntax error, last name: x\n" +
				"4: x\t(\n\tsyntax error, last name: x\n5: x\t(\n\tsyntax error, last name: x\n" +
				"6: x\t(\n\tsyntax error, last name: x\n7: x\t(\n\tsyntax error, last name: x\n" +
				"too many errors\n", "fail:errors"},
		{"no file", "", []string{"units", "/tmp/cmds2b/none"},
			"units: cannot open /tmp/cmds2b/none: file does not exist\n", "fail:open"},
		{"usage", "", []string{"units", "-x"}, "usage: units [-v] [file]\n", "fail:usage"},
	})
}

func TestCmds2BCalc(t *testing.T) {
	bScratch(t, map[string]string{
		"prog": "fn sq(x) { return x*x }\nprint sq(3), \"\\n\"\ninclude \"/tmp/cmds2b/inc\"\nz\n",
		"inc":  "z = 42\n",
	})
	bRun(t, []bCase{
		{"args", "", []string{"calc", "2", "+", "3", "*", "4"}, "14\n", ""},
		{"expr arg", "", []string{"calc", "1/4"}, ".25\n", ""},
		{"sigma", "sigma(i = 0, 100, 1/i!)\n", []string{"calc"}, "2.7182818284590455\n", ""},
		{"integral", "integral(x = -1.96, 1.96, exp(-0.5*x*x)/sqrt(2*Pi))\n", []string{"calc"},
			".9500042096998785\n", ""},
		{"differential", "differential(x=1, x*x+5*x+6)\n", []string{"calc"}, "7\n", ""},
		{"solve", "solve(x**2-5*x+6==0)\n", []string{"calc"}, "2 3 \n3\n", ""},
		{"no roots", "solve(x*x+1)\n", []string{"calc"}, "error: line 1: no roots found\n", ""},
		{"ack", "fn ack(a, b)\n{\n\tif(a == 0)\n\t\treturn b+1;\n\tif(b == 0)\n\t\treturn ack(a-1, 1);\n" +
			"\treturn ack(a-1, ack(a, b-1));\n}\nack(2, 3)\n", []string{"calc"}, "9\n", ""},
		{"loops", "s = 0; for(i = 1; i <= 10; i++) s += i; s\nwhile(i > 5) { i-- }\ni\ndo { i = i*2 } while(i < 100)\ni\n",
			[]string{"calc"}, "55\n5\n160\n", ""},
		{"printbase", "printbase = 16\n255\n-0.5\nprintbase = 2\n5\n", []string{"calc"}, "16rff\n-16r0.8\n2r101\n", ""},
		{"numbers", "0x1f\n017\n2r101\n1.5e3\n'a'\n1e6\n100000\n0.00001\n", []string{"calc"},
			"31\n15\n5\n1500\n97\n1e+06\n100000\n1e-05\n", ""},
		{"operators", "7 % 3\n7 // 2\n2 ** 10\n5!\n6 & 3\n6 | 3\n6 ^ 3\n1 << 4\n3 > 2 ? 10 : 20\n1 <-> 0\n",
			[]string{"calc"}, "1\n3\n1024\n120\n2\n7\n5\n16\n10\n0\n", ""},
		{"degrees", "degrees = 1\nsin 90\natan2(1, 1)\n", []string{"calc"}, "1\n45\n", ""},
		{"constants", "Pi\nInfinity\nNaN\n", []string{"calc"}, "3.141592653589793\nInfinity\nNaN\n", ""},
		{"zero divide", "7 % 0\n1/0\n", []string{"calc"}, "error: zero divide\nInfinity\n", ""},
		{"errors", "1 +\n\"str\"\n$\n", []string{"calc"},
			"error: line 1: missing expression\nerror: line 2: illegal string operation\nerror: line 3: bad character $\n", ""},
		{"args count", "sqrt(1, 2)\n", []string{"calc"}, "error: line 1: wrong number of arguments\n", ""},
		{"strict", "", []string{"calc", "-s", "y+1"}, "error: line 1: y undefined\n", ""},
		{"strict declared", "", []string{"calc", "-s", "y := 2; y+1"}, "3\n", ""},
		{"break", "break\n", []string{"calc"}, "error: line 1: break/continue not in loop\n", ""},
		{"eof", "{ 1\n", []string{"calc"}, "error: premature eof\n", ""},
		{"exit", "1\nexit\n2\n", []string{"calc"}, "1\n", ""},
		{"file", "", []string{"calc", "/tmp/cmds2b/prog"}, "9\n42\n", ""},
		{"include missing", "include \"/tmp/cmds2b/none\"\n", []string{"calc"},
			"error: line 1: cannot open /tmp/cmds2b/none\n", ""},
		{"read", "read a\n4\na*2\n", []string{"calc"}, "a ? 8\n", ""},
	})
}
