// golive-quote-config writes reviewable full-catalog configuration drafts. It
// never contacts a service or changes the default plan without an explicit flag.
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"

	"github.com/chuanmingliu/golive/internal/crm"
	"github.com/chuanmingliu/golive/internal/quotesvc"
)

func main() {
	kind := flag.String("kind", "", "plans, api, or rating")
	input := flag.String("input", "", "optional existing JSON document (preserve other settings)")
	output := flag.String("output", "", "new draft path, must not exist")
	activate := flag.Bool("make-default", false, "make motor-full the default in a plans draft")
	flag.Parse()
	if err := run(*kind, *input, *output, *activate); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
func run(kind, input, output string, activate bool) error {
	if output == "" {
		return fmt.Errorf("-output is required; drafts are never printed with private settings")
	}
	doc := map[string]any{}
	if input != "" {
		raw, err := os.ReadFile(input)
		if err != nil {
			return err
		}
		if err = json.Unmarshal(raw, &doc); err != nil {
			return err
		}
		if doc == nil {
			return fmt.Errorf("input must be an object")
		}
	}
	switch kind {
	case "plans":
		plans, ok := doc["plans"].(map[string]any)
		if !ok {
			plans = map[string]any{}
			doc["plans"] = plans
		}
		if _, exists := plans["motor-full"]; exists {
			return fmt.Errorf("motor-full already exists; review its definition manually")
		}
		plans["motor-full"] = crm.FullMotorPlan()
		if activate || doc["default"] == nil {
			doc["default"] = "motor-full"
		}
	case "api":
		api := crm.FullMotorAPI()
		coverages, ok := doc["coverages"].(map[string]any)
		if !ok {
			coverages = map[string]any{}
		}
		for k, v := range api.Coverages {
			coverages[k] = v
		}
		doc["coverages"] = coverages
		doc["tax_field"] = api.TaxField
	case "rating":
		var rating map[string]any
		_ = json.Unmarshal([]byte(quotesvc.DefaultRating), &rating)
		if input == "" {
			doc = rating
		} else {
			coverages, ok := doc["coverages"].(map[string]any)
			if !ok {
				return fmt.Errorf("rating input requires coverages")
			}
			for k, v := range rating["coverages"].(map[string]any) {
				if old, ok := coverages[k].(map[string]any); ok {
					// Preserve existing rates. Add only missing synthetic table tiers.
					if table, ok := old["table"].(map[string]any); ok {
						if next, ok := v.(map[string]any)["table"].(map[string]any); ok {
							for tier, price := range next {
								if _, exists := table[tier]; !exists {
									table[tier] = price
								}
							}
						}
					}
				} else {
					coverages[k] = v
				}
			}
		}
	default:
		return fmt.Errorf("-kind must be plans, api, or rating")
	}
	raw, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		return err
	}
	file, err := os.OpenFile(output, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return err
	}
	if _, err = file.Write(append(raw, '\n')); err != nil {
		file.Close()
		return err
	}
	return file.Close()
}
