package dao

import "testing"

// TestEscapeLike 通配符转义：%/ _ 与反斜杠本身均需转义，防止成本注入。
func TestEscapeLike(t *testing.T) {
	cases := map[string]string{
		`plain`:      `plain`,
		`100%`:       `100\%`,
		`a_b`:        `a\_b`,
		`back\slash`: `back\\slash`,
	}
	for in, want := range cases {
		if got := EscapeLike(in); got != want {
			t.Errorf("EscapeLike(%q) = %q, want %q", in, got, want)
		}
	}
	pattern, esc := LikeContains("50%_off")
	if pattern != `%50\%\_off%` || esc != `ESCAPE '\'` {
		t.Errorf("LikeContains = (%q, %q)", pattern, esc)
	}
}
