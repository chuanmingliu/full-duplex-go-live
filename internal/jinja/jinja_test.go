package jinja

import (
	"encoding/json"
	"os"
	"strings"
	"testing"
)

type refCase struct {
	Template string         `json:"template"`
	Vars     map[string]any `json:"vars"`
	Want     *string        `json:"want"`
	Error    string         `json:"error"`
}

func readCases(t *testing.T, path string) []refCase {
	t.Helper()
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	var file struct {
		Cases []refCase `json:"cases"`
	}
	dec := json.NewDecoder(f)
	dec.UseNumber()
	if err := dec.Decode(&file); err != nil {
		t.Fatal(err)
	}
	return file.Cases
}

func render(c refCase) (string, error) {
	tpl, err := Parse(c.Template)
	if err != nil {
		return "", err
	}
	return tpl.Render(c.Vars)
}

// Every case in testdata/cases.json was rendered by Jinja2 itself
// (testdata/gen.py). This package must write the same text, character for
// character, and must fail wherever Jinja2 fails.
func TestRendersAsJinja2Does(t *testing.T) {
	cases := readCases(t, "testdata/cases.json")
	if len(cases) < 1000 {
		t.Fatalf("only %d reference cases", len(cases))
	}
	wrong := 0
	for i, c := range cases {
		got, err := render(c)
		vars, _ := json.Marshal(c.Vars)
		switch {
		case c.Error != "" && err == nil:
			wrong++
			t.Errorf("case %d: %s\n  with %s\n  Jinja2 fails (%s); this rendered %q", i, c.Template, vars, c.Error, got)
		case c.Error == "" && err != nil:
			wrong++
			t.Errorf("case %d: %s\n  with %s\n  Jinja2 renders %q; this failed: %v", i, c.Template, vars, *c.Want, err)
		case c.Error == "" && got != *c.Want:
			wrong++
			t.Errorf("case %d: %s\n  with %s\n  Jinja2: %q\n  this:   %q", i, c.Template, vars, *c.Want, got)
		}
		if wrong >= 25 {
			t.Fatalf("stopping after %d differences", wrong)
		}
	}
}

// A prompt kept outside the repository can be checked the same way: set
// JINJA_PRIVATE_CASES to a file of the same shape (testdata/gen.py's format).
func TestAPrivateTemplateRendersAsJinja2Does(t *testing.T) {
	path := os.Getenv("JINJA_PRIVATE_CASES")
	if path == "" {
		t.Skip("JINJA_PRIVATE_CASES is not set")
	}
	cases := readCases(t, path)
	for i, c := range cases {
		got, err := render(c)
		if c.Error != "" {
			if err == nil {
				t.Errorf("case %d: Jinja2 fails (%s); this rendered %d characters", i, c.Error, len([]rune(got)))
			}
			continue
		}
		if err != nil {
			t.Fatalf("case %d: %v", i, err)
		}
		if got != *c.Want {
			// Say where, not what: the template is private.
			a, b := []rune(got), []rune(*c.Want)
			at := 0
			for at < len(a) && at < len(b) && a[at] == b[at] {
				at++
			}
			t.Fatalf("case %d: differs from Jinja2 at character %d of %d (this has %d)", i, at, len(b), len(a))
		}
	}
	t.Logf("%d private cases agree", len(cases))
}

func TestNamesAreWhatACallerMustSupply(t *testing.T) {
	tpl, err := Parse(`{% set years = (months | int // 12) %}{{ years }}{% if not is_sh %}{{ plate }}{% endif %}` +
		`{% for item in things %}{{ item }}{{ loop.index }}{{ label }}{% endfor %}{{ now.split(' ')[0] if ' ' in now else start }}`)
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(tpl.Names(), ","); got != "months,is_sh,plate,things,label,now,start" {
		t.Errorf("names: %s", got)
	}
}

func TestFirstVariableIsWhereRendersCanStartToDiffer(t *testing.T) {
	for src, want := range map[string]int{
		"固定的开头，{# a note #}还是固定的。{% if x %}变{% endif %}": 24,
		"all of it fixed":        15,
		"{{ x }} from the first": 0,
		"十个字的固定开头啊啊{{ x }}":      10,
	} {
		tpl, err := Parse(src)
		if err != nil {
			t.Fatal(err)
		}
		if got := tpl.FirstVariable(); got != want {
			t.Errorf("%q: first variable at %d, want %d", src, got, want)
		}
	}
}

func TestErrorsSayWhere(t *testing.T) {
	_, err := Parse("fine so far {% include 'x' %}")
	e, ok := err.(*Error)
	if !ok || e.Pos != 12 || !strings.Contains(e.Msg, "include") {
		t.Errorf("error: %#v", err)
	}
}
