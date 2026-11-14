package names

import "testing"

func TestCleanname(t *testing.T) {
	cases := map[string]string{
		"":               ".",
		"/":              "/",
		"a/b/../c":       "a/c",
		"//a///b/":       "/a/b",
		"/..":            "/",
		"../..":          "../..",
		"a/../..":        "..",
		"./a/./b":        "a/b",
		"#c":             "#c",
		"#c/cons/../x":   "#c/x",
		"#c/..":          "#c",
		"#Ufoo/a/../b":   "#Ufoo/b",
		"#":              "#",
		"a/b/c/../../..": ".",
		"/a/b/../../..":  "/",
	}
	for in, want := range cases {
		if got := Cleanname(in); got != want {
			t.Errorf("Cleanname(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestDirBase(t *testing.T) {
	if d := Dirname("/a/b"); d != "/a" {
		t.Errorf("Dirname = %q", d)
	}
	if d := Dirname("/a"); d != "/" {
		t.Errorf("Dirname root = %q", d)
	}
	if d := Dirname("a"); d != "" {
		t.Errorf("Dirname none = %q", d)
	}
	if b := Basename("/a/b.c", ".c"); b != "b" {
		t.Errorf("Basename = %q", b)
	}
	if b := Basename("/a/.c", ".c"); b != "" {
		t.Errorf("Basename whole suffix = %q", b)
	}
}
