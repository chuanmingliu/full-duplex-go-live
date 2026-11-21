package main

import "errors"

const defaultSkillSelectionThreshold = 50

type SkillSelectionConfig struct {
	Mode      string `json:"mode,omitempty"`
	Threshold *int   `json:"threshold,omitempty"`
}

func (c Catalog) selectionSettings() (string, int) {
	mode, threshold := "auto", defaultSkillSelectionThreshold
	if c.SkillSelection != nil {
		if c.SkillSelection.Mode != "" {
			mode = c.SkillSelection.Mode
		}
		if c.SkillSelection.Threshold != nil {
			threshold = *c.SkillSelection.Threshold
		}
	}
	return mode, threshold
}

func (c Catalog) validateSelectionSettings() error {
	mode, threshold := c.selectionSettings()
	if mode != "auto" && mode != "always" && mode != "off" {
		return errors.New("skill_selection.mode must be auto, always or off")
	}
	if threshold < 0 || threshold > maxCatalogSkills {
		return errors.New("skill_selection.threshold must be between 0 and 100000")
	}
	return nil
}

type skillSelectionState struct {
	mode      string
	threshold int
	available int
	enabled   bool
}

func (c Catalog) selectionState() skillSelectionState {
	mode, threshold := c.selectionSettings()
	state := skillSelectionState{mode: mode, threshold: threshold}
	for _, skill := range c.Skills {
		if c.available(skill) {
			state.available++
		}
	}
	state.enabled = mode == "always" || (mode == "auto" && state.available > threshold)
	return state
}

func (x *skillIndex) selectionCapabilities() map[string]any {
	state := x.selection
	capabilities := map[string]any{
		"version": "1", "enabled": state.enabled, "mode": state.mode,
		"threshold": state.threshold, "available_skills": state.available,
		"methods": []string{},
	}
	if state.enabled {
		capabilities["methods"] = []string{"skills/search", "skills/describe", "skills/resolve", "skills/load"}
		capabilities["max_reply_bytes"] = maxSelectionBytes
		capabilities["search_modes"] = []string{"auto", "semantic", "lexical"}
		capabilities["semantic_available"] = x.semantic != nil || x.pool != nil
	}
	return capabilities
}
