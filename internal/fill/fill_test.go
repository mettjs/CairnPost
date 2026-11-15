package fill

import (
	"errors"
	"slices"
	"strings"
	"testing"
)

func TestNamesAreFoundOnceInOrder(t *testing.T) {
	got, err := Names("Hi {{name}}, {{ code }} is yours, {{name}}. {{_a1}}")
	if err != nil || !slices.Equal(got, []string{"name", "code", "_a1"}) {
		t.Fatalf("%v %v", got, err)
	}
	if got, err := Names("no variables } } {single} }}"); err != nil || len(got) != 0 {
		t.Fatalf("%v %v", got, err)
	}
}

func TestAnOpeningThatNamesNothingIsRefused(t *testing.T) {
	for text, nth := range map[string]int{
		"{{": 1, "{{}}": 1, "{{ }}": 1, "{{1a}}": 1, "{{a b}}": 1, "{{a.b}}": 1, "{{a": 1, "{{a}": 1,
		"{{\na}}": 1, "{{a}} {{#if}}": 2, "{{{a}}}": 1, "{{a}}{{b}}{{-}}": 3,
		"{{" + strings.Repeat("a", MaxName+1) + "}}": 1,
	} {
		var bad *Malformed
		if _, err := Names(text); !errors.As(err, &bad) || bad.Nth != nth {
			t.Errorf("%q: %v", text, err)
		}
		if _, err := Text(text, nil, 100); err == nil {
			t.Errorf("%q was filled", text)
		}
		if strings.Contains((&Malformed{Nth: nth}).Error(), text) {
			t.Errorf("the error quotes %q", text)
		}
	}
	if _, err := Names("{{" + strings.Repeat("a", MaxName) + "}}"); err != nil {
		t.Error(err)
	}
}

func TestValuesAreWrittenAsTheyAreInTextAndEscapedInHTML(t *testing.T) {
	values := map[string]string{"name": `<b>Ana & "Bo"</b>`, "again": "{{name}}"}
	text, err := Text("Hi {{name}}: {{again}}{{none}}.", values, 100)
	if err != nil || text != `Hi <b>Ana & "Bo"</b>: {{name}}.` {
		t.Fatalf("%q %v", text, err)
	}
	page, err := HTML(`<p title="{{name}}">{{ name }}</p>{{again}}`, values, 200)
	want := `<p title="&lt;b&gt;Ana &amp; &#34;Bo&#34;&lt;/b&gt;">&lt;b&gt;Ana &amp; &#34;Bo&#34;&lt;/b&gt;</p>{{name}}`
	if err != nil || page != want {
		t.Fatalf("%q %v", page, err)
	}
}

func TestWhatCanNameAVariable(t *testing.T) {
	for s, want := range map[string]bool{"a": true, "_": true, "A_1": true, "": false, "1a": false, "a-b": false, "a b": false,
		"é": false, strings.Repeat("a", MaxName): true, strings.Repeat("a", MaxName+1): false} {
		if Name(s) != want {
			t.Errorf("%q: %v", s, !want)
		}
	}
}

func TestATextFilledTooLongIsNotWritten(t *testing.T) {
	values := map[string]string{"v": strings.Repeat("x", 10)}
	if got, err := Text("{{v}}{{v}}", values, 20); err != nil || len(got) != 20 {
		t.Fatalf("%q %v", got, err)
	}
	for _, text := range []string{"{{v}}{{v}}.", "{{v}}{{v}}{{v}}", strings.Repeat("{{v}}", 1000)} {
		if _, err := Text(text, values, 20); !errors.Is(err, ErrTooLong) {
			t.Errorf("%q: %v", text[:10], err)
		}
	}
	// A value that cannot fit is not escaped first to find that out.
	if _, err := HTML("{{v}}", map[string]string{"v": strings.Repeat(`"`, 1<<20)}, 100); !errors.Is(err, ErrTooLong) {
		t.Error(err)
	}
	// Escaping counts: what is written is what is measured.
	if _, err := HTML("{{v}}", map[string]string{"v": "<<<<<"}, 10); !errors.Is(err, ErrTooLong) {
		t.Error(err)
	}
}
