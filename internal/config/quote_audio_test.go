package config

import "testing"

func TestQuoteAudioPreparationLimit(t *testing.T) {
	if Default().Duplex.QuoteReadoutWarmMax != 0 {
		t.Fatal("quote audio preparation must default off")
	}
	for _, value := range []int{-1, 0, 1, 4, 8, 9} {
		cfg := Default()
		cfg.Duplex.QuoteReadoutWarmMax = value
		err := cfg.Validate()
		if (err == nil) != (value >= 0 && value <= 8) {
			t.Fatalf("limit %d: %v", value, err)
		}
	}
}
