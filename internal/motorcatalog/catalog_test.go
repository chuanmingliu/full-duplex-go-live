package motorcatalog

import "testing"

func TestUnitsAndDistinctRiders(t *testing.T) {
	entries := Entries()
	byName := map[string]Entry{}
	for _, e := range entries {
		byName[e.Name] = e
	}
	for _, tc := range []struct{ name, in, want string }{
		{"三者险", "300w", "300万"}, {"三者险", "300", "300万"}, {"三者险", "3000000", "300万"},
		{"划痕险", "1w", "1万"}, {"划痕险", "10000元", "1万"}, {"绝对免赔率-司机", "10％", "10%"},
		{"医保外用药责任险-三者", "共享保额", "共享保额"},
	} {
		e := byName[tc.name]
		if got := Normalize(tc.in, e.Detail, e.Options); got != tc.want {
			t.Errorf("%s/%s: %v", tc.name, tc.in, got)
		}
	}
	for _, s := range []string{"5%", "garbage300万", "300万以上", "NaN", "Inf"} {
		if _, ok := Amount(s); ok {
			t.Errorf("invalid amount: %s", s)
		}
	}
	for _, e := range entries {
		if got := Normalize("不投保", e.Detail, e.Options); got != false {
			t.Error(e.Name)
		}
	}
}
