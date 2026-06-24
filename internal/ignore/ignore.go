// Package ignore translates gitignore-style patterns into ordered rsync filter
// rules. gitignore is last-match-wins; rsync is first-match-wins, so the order
// is reversed during translation.
package ignore

import "strings"

// ToRsyncFilters converts patterns into the strings passed as repeated --filter
// arguments. "!p" becomes an include ("+ p"); any other pattern becomes an
// exclude ("- p"). Blank lines and lines starting with '#' are skipped.
func ToRsyncFilters(patterns []string) []string {
	var out []string
	for i := len(patterns) - 1; i >= 0; i-- {
		p := strings.TrimSpace(patterns[i])
		if p == "" || strings.HasPrefix(p, "#") {
			continue
		}
		if strings.HasPrefix(p, "!") {
			out = append(out, "+ "+strings.TrimSpace(p[1:]))
		} else {
			out = append(out, "- "+p)
		}
	}
	return out
}
