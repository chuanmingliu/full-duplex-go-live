// Package simulatorpresets embeds the reviewed synthetic customer scenarios.
package simulatorpresets

import (
	"embed"
	"fmt"
	"io/fs"
	"strings"

	"github.com/chuanmingliu/golive/internal/simulator"
)

//go:embed *.json
var files embed.FS

// All returns validated independent copies; callers cannot mutate the embedded files.
func All() ([]simulator.Scenario, error) {
	names, err := fs.Glob(files, "*.json")
	if err != nil {
		return nil, err
	}
	out := make([]simulator.Scenario, 0, len(names))
	for _, name := range names {
		raw, err := files.ReadFile(name)
		if err != nil {
			return nil, err
		}
		scenario, err := simulator.DecodeScenario(strings.NewReader(string(raw)))
		if err != nil {
			return nil, fmt.Errorf("preset %s: %w", name, err)
		}
		out = append(out, scenario)
	}
	return out, nil
}
