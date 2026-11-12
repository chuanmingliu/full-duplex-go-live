package motorcatalog

import (
	"reflect"
	"strings"
	"testing"
)

// This expectation is transcribed from the owner's product requirements,
// independently of catalog.json. Generated matrix tests alone cannot detect a
// missing tier because their inputs are generated from that same catalog.
func TestCatalogMatchesRequestedCoverageContract(t *testing.T) {
	rows := []struct{ name, kind, parent, options string }{
		{"车损险", "boolean", "", ""},
		{"划痕险", "amount", "车损险", "2000,5000,1万,2万"},
		{"绝对免赔率-车损", "percent", "车损险", "5%,10%,15%,20%"},
		{"三者险", "amount", "", "50万,100万,150万,200万,300万,400万"},
		{"精神损害险-三者", "amount", "三者险", "1万,2万,5万,10万"},
		{"假日三者翻倍险", "boolean", "三者险", ""},
		{"绝对免赔率-三者", "percent", "三者险", "5%,10%,15%,20%"},
		{"医保外用药责任险-三者", "shared_amount", "三者险", "共享保额,10万,15万,20万,30万,50万,100万,150万,200万,300万,400万"},
		{"司机责任险", "amount", "", "2万,3万,4万,5万,10万,20万"},
		{"精神损害险-司机", "amount", "司机责任险", "1万,2万,5万,10万"},
		{"绝对免赔率-司机", "percent", "司机责任险", "5%,10%,15%,20%"},
		{"医保外用药责任险-司机", "shared_amount", "司机责任险", "共享保额,1万,2万,5万,10万,20万"},
		{"乘客责任险", "amount", "", "2万,3万,4万,5万,10万,20万"},
		{"精神损害险-乘客", "amount", "乘客责任险", "1万,2万,5万,10万"},
		{"绝对免赔率-乘客", "percent", "乘客责任险", "5%,10%,15%,20%"},
		{"医保外用药责任险-乘客", "shared_amount", "乘客责任险", "共享保额,1万,2万,5万,10万,20万"},
		{"交强险", "boolean", "", ""},
		{"车船税", "enum", "", "代缴"},
	}
	actual := map[string]Entry{}
	for _, entry := range Entries() {
		if _, exists := actual[entry.Name]; exists {
			t.Fatalf("duplicate coverage %s", entry.Name)
		}
		actual[entry.Name] = entry
	}
	if len(actual) != len(rows) {
		t.Fatalf("got %d coverages; want %d", len(actual), len(rows))
	}
	for _, row := range rows {
		t.Run(row.name, func(t *testing.T) {
			entry, ok := actual[row.name]
			if !ok {
				t.Fatal("required coverage missing")
			}
			if entry.Kind != row.kind || entry.Parent != row.parent {
				t.Fatalf("kind/parent=%s/%s; want %s/%s", entry.Kind, entry.Parent, row.kind, row.parent)
			}
			want := []string{}
			if row.options != "" {
				want = strings.Split(row.options, ",")
			}
			if !reflect.DeepEqual(entry.Options, want) {
				t.Errorf("options=%v; want %v", entry.Options, want)
			}
			if Normalize("不投保", entry.Detail, entry.Options) != false {
				t.Error("declining coverage is not supported")
			}
		})
	}
}
