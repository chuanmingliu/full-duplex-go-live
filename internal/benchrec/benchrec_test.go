package benchrec

import (
	"testing"
	"time"
)

func TestARecordRoundTripsAndJudgesItsThresholds(t *testing.T) {
	dir := t.TempDir()
	r := Record{Layer: "L2", Build: "abc1234", Source: "mocks", At: time.Date(2026, 10, 21, 17, 0, 0, 0, time.UTC),
		Metrics: map[string]float64{"S_BC": 0.95, "p95_ms": 1300}, Thresholds: map[string]string{"S_BC": ">=0.9", "p95_ms": "<=1200"}}
	if _, err := Write(dir, r); err != nil {
		t.Fatal(err)
	}
	got, err := Read(dir)
	if err != nil || len(got) != 1 || got[0].Build != "abc1234" {
		t.Fatalf("read %v %v", got, err)
	}
	v := got[0].Verdicts()
	if !v["S_BC"] || v["p95_ms"] {
		t.Fatalf("verdicts %v", v)
	}
}
