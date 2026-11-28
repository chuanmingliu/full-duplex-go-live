package quotesvc

import (
	"encoding/json"
	"strconv"
	"testing"

	"github.com/chuanmingliu/golive/internal/motorcatalog"
)

func TestMotorBackendEveryTier(t *testing.T) {
	var rating Rating
	if err := json.Unmarshal([]byte(DefaultRating), &rating); err != nil {
		t.Fatal(err)
	}
	entries := motorcatalog.Entries()
	byName := map[string]motorcatalog.Entry{}
	for _, e := range entries {
		byName[e.Name] = e
	}
	makeCoverage := func(e motorcatalog.Entry, value string) Coverage {
		c := Coverage{CoverageCode: e.Code, SumInsured: "1"}
		if e.Parent != "" {
			c.ParentCoverageCode = byName[e.Parent].Code
		}
		switch {
		case e.Kind == "percent":
			c.SumInsured = ""
			c.DeductibleRate = value[:len(value)-1]
		case value == "共享保额":
			c.SumInsured = "shared"
		case e.Kind != "boolean":
			n, _ := motorcatalog.Amount(value)
			c.SumInsured = strconv.FormatFloat(n, 'f', -1, 64)
		}
		return c
	}
	count := 0
	for _, e := range entries {
		if e.Code == "" {
			continue
		}
		options := e.Options
		if e.Kind == "boolean" {
			options = []string{"投保"}
		}
		for _, o := range options {
			t.Run(e.Name+"/"+o, func(t *testing.T) {
				// Deliberately put the rider first; dependency evaluation must order it.
				in := Inquiry{CoverageList: []Coverage{makeCoverage(e, o)}}
				if e.Parent != "" {
					p := byName[e.Parent]
					choice := "投保"
					if len(p.Options) > 0 {
						choice = p.Options[len(p.Options)-1]
					}
					in.CoverageList = append(in.CoverageList, makeCoverage(p, choice))
				}
				lines, _, _, err := rating.priceLines(in, Vehicle{Price: 150000, Seats: 5}, 0.95)
				if err != nil {
					t.Fatal(err)
				}
				line := lines[0]
				if line.CoverageCode != e.Code {
					t.Fatal(line)
				}
				if e.Kind == "percent" && line.Tag != o {
					t.Fatal("lost deductible", line)
				}
				if o == "共享保额" && (line.Tag != o || line.Amount != lines[1].Amount) {
					t.Fatal("shared amount not inherited", lines)
				}
				if e.Kind == "amount" || e.Kind == "shared_amount" && o != "共享保额" {
					want, _ := motorcatalog.Amount(o)
					if line.Amount != strconv.FormatFloat(want, 'f', -1, 64) {
						t.Fatalf("requested %s, priced %s", o, line.Amount)
					}
				}
				count++
			})
		}
	}
	t.Logf("priced %d configured business choices", count)
	if _, _, _, err := rating.priceLines(Inquiry{CoverageList: []Coverage{{CoverageCode: "missing", SumInsured: "10000"}}}, Vehicle{}, 1); err == nil {
		t.Fatal("unknown coverage silently dropped")
	}
	e := byName["精神损害险-司机"]
	if _, _, _, err := rating.priceLines(Inquiry{CoverageList: []Coverage{makeCoverage(e, "1万")}}, Vehicle{}, 1); err == nil {
		t.Fatal("orphan rider priced")
	}
}
