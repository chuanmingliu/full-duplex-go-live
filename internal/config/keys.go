package config

import (
	"bufio"
	"fmt"
	"os"
	"strings"
)

// EnvKeys gathers the keys for a provider whose single key lives in keyEnv.
//
// Keys come from three places, in this order, with duplicates dropped:
//
//	<keyEnv>=…                  the one there has always been
//	<keyEnv>S=…,…,…             more, separated by commas or spaces
//	<keyEnv>S_FILE=/path/file   more, one a line; # starts a comment
//
// The first key is the account a provider uses when it is asked for nothing in
// particular; the rest are the accounts beside it. Nothing ever logs a key: a
// key is referred to by its position in this list.
func EnvKeys(keyEnv string) ([]string, error) {
	var keys []string
	seen := map[string]bool{}
	add := func(k string) {
		k = strings.Trim(strings.TrimSpace(k), `"'`)
		if k != "" && !seen[k] {
			seen[k] = true
			keys = append(keys, k)
		}
	}
	add(os.Getenv(keyEnv))
	for _, k := range strings.FieldsFunc(os.Getenv(keyEnv+"S"), func(r rune) bool {
		return r == ',' || r == ';' || r == ' ' || r == '\t' || r == '\n'
	}) {
		add(k)
	}
	if path := Env(keyEnv+"S_FILE", ""); path != "" {
		f, err := os.Open(path)
		if err != nil {
			return nil, fmt.Errorf("config: %sS_FILE: %w", keyEnv, err)
		}
		defer f.Close()
		sc := bufio.NewScanner(f)
		for sc.Scan() {
			line := sc.Text()
			if i := strings.IndexByte(line, '#'); i >= 0 {
				line = line[:i]
			}
			add(line)
		}
	}
	if len(keys) == 0 {
		return nil, fmt.Errorf("config: %s is required but not set (or %sS, or %sS_FILE)", keyEnv, keyEnv, keyEnv)
	}
	return keys, nil
}
