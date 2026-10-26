package main

import (
	"errors"
	"fmt"
	"os"
	"regexp"
	"strconv"
	"strings"
)

// flexInt is a whole number that may be written as 10 or "10" in JSON. Panels that rewrite config files
// often turn every value into a string.
type flexInt int

func (f *flexInt) UnmarshalJSON(b []byte) error {
	s := strings.TrimSpace(strings.Trim(strings.TrimSpace(string(b)), `"`))
	if s == "" || s == "null" {
		return nil // keep the default
	}
	n, err := strconv.Atoi(s)
	if err != nil {
		return fmt.Errorf("%q is not a whole number", s)
	}
	*f = flexInt(n)
	return nil
}

// placeholder matches {name} and {name:default}.
var placeholder = regexp.MustCompile(`\{([A-Za-z_][A-Za-z0-9_]*)(?::([^{}]*))?\}`)

// expand replaces {name} with the environment variable NAME (Pterodactyl/Pelican expose egg variables
// that way), or with the text after the colon in {name:default} when the variable is unset or empty.
// {port} is left alone: the launcher fills it in with the backend port.
func expand(s string) (string, error) {
	var missing error
	out := placeholder.ReplaceAllStringFunc(s, func(m string) string {
		sub := placeholder.FindStringSubmatch(m)
		name := sub[1]
		if strings.EqualFold(name, "port") {
			return m
		}
		for _, key := range []string{strings.ToUpper(name), name} {
			if v := os.Getenv(key); v != "" {
				return v
			}
		}
		if strings.Contains(m, ":") {
			return sub[2]
		}
		missing = errors.Join(missing, fmt.Errorf("environment variable %s is not set (needed by %s)", strings.ToUpper(name), m))
		return m
	})
	return out, missing
}

// expandAll applies expand to every string value in decoded JSON.
func expandAll(v any) (any, error) {
	switch t := v.(type) {
	case string:
		return expand(t)
	case []any:
		var errs error
		for i := range t {
			var err error
			if t[i], err = expandAll(t[i]); err != nil {
				errs = errors.Join(errs, err)
			}
		}
		return t, errs
	case map[string]any:
		var errs error
		for k := range t {
			var err error
			if t[k], err = expandAll(t[k]); err != nil {
				errs = errors.Join(errs, err)
			}
		}
		return t, errs
	}
	return v, nil
}
