package crm

import (
	"encoding/json"
	"fmt"
	"sort"

	"github.com/chuanmingliu/golive/internal/motorcatalog"
)

// Family enumeration is exhaustive within each main coverage and its riders.
// The full policy cross-product has hundreds of billions of members.
func motorFamilyStates(root motorcatalog.Entry, entries []motorcatalog.Entry) []map[string]any {
	off := map[string]any{root.Name: false}
	children := []motorcatalog.Entry{}
	for _, entry := range entries {
		if entry.Parent == root.Name {
			children = append(children, entry)
			off[entry.Name] = false
		}
	}
	states := []map[string]any{off}
	for _, choice := range motorChoices(root)[1:] {
		combinations := []map[string]any{{root.Name: choice}}
		for _, child := range children {
			next := []map[string]any{}
			for _, current := range combinations {
				for _, childState := range motorFamilyStates(child, entries) {
					merged := copyVars(current)
					for key, value := range childState {
						merged[key] = value
					}
					next = append(next, merged)
				}
			}
			combinations = next
		}
		states = append(states, combinations...)
	}
	return states
}

func motorChoices(entry motorcatalog.Entry) []any {
	values := []any{false}
	if entry.Kind == "boolean" {
		return append(values, true)
	}
	for _, option := range entry.Options {
		values = append(values, option)
	}
	return values
}

type motorCombination struct {
	ID        string         `json:"id"`
	Selection map[string]any `json:"selection"`
}

func motorCombinationMatrix() ([]motorCombination, map[string]int, uint64) {
	entries := motorcatalog.Entries()
	plan := FullMotorPlan()
	unique := map[string]map[string]any{}
	add := func(selection map[string]any) {
		normalized := copyVars(selection)
		plan.NormalizeCoverages(normalized)
		canonical := selectedConfig(normalized)
		raw, _ := json.Marshal(canonical)
		unique[string(raw)] = canonical
	}
	counts := map[string]int{}
	product := uint64(1)
	for _, root := range entries {
		if root.Parent != "" {
			continue
		}
		states := motorFamilyStates(root, entries)
		counts[root.Name] = len(states)
		product *= uint64(len(states))
		for _, state := range states {
			selection := copyVars(plan.DefaultSelection)
			for key, value := range state {
				selection[key] = value
			}
			add(selection)
		}
	}
	// Exercise every compatible pair of field choices in an otherwise fully
	// selected policy. Declining a parent also declines its unmentioned riders.
	for i, first := range entries {
		for _, second := range entries[i+1:] {
			for _, firstChoice := range motorChoices(first) {
				for _, secondChoice := range motorChoices(second) {
					selection := motorSelection()
					selection[first.Name], selection[second.Name] = firstChoice, secondChoice
					valid := true
					for _, entry := range entries {
						if entry.Parent == "" || chosen(selection, entry.Parent) {
							continue
						}
						if (entry.Name == first.Name || entry.Name == second.Name) && chosen(selection, entry.Name) {
							valid = false
							break
						}
						selection[entry.Name] = false
					}
					if valid {
						add(selection)
					}
				}
			}
		}
	}
	keys := make([]string, 0, len(unique))
	for key := range unique {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	cases := make([]motorCombination, 0, len(keys))
	for index, key := range keys {
		cases = append(cases, motorCombination{ID: fmt.Sprintf("motor-%05d", index+1), Selection: unique[key]})
	}
	return cases, counts, product
}
