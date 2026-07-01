// Package ignore translates gitignore-style patterns into ordered rsync filter
// rules. gitignore is last-match-wins; rsync is first-match-wins, so the order
// is reversed during translation.
package ignore

import "strings"

// ToRsyncFilters converts patterns into the strings passed as repeated --filter
// arguments. "!p" becomes an include ("+ p"); any other pattern becomes an
// exclude ("- p"). Blank lines and lines starting with '#' are skipped, and the
// list is emitted in reverse so gitignore's last-match-wins maps onto rsync's
// first-match-wins.
//
// The pattern BODY is forwarded to rsync verbatim; gsync does not rewrite the
// glob. This works because gsync syncs a single remote root to a single local
// directory, so rsync's filter-rule semantics line up with gitignore's for the
// common cases:
//
//   - a leading "/" (e.g. "/foo") anchors to the transfer root, which is the
//     sync root — matching gitignore's "top-level only" meaning here;
//   - a pattern with a non-trailing slash (e.g. "a/b") is anchored in BOTH
//     systems, so it is not matched at arbitrary depth;
//   - a slash-less pattern (e.g. "*.log") matches at every directory level in
//     both systems (basename match);
//   - a trailing "/" (e.g. "build/") is directory-only in both systems;
//   - "**" is forwarded and honored by rsync (it crosses "/", unlike "*").
//
// Two gitignore behaviors are NOT reproduced and are worth knowing:
//
//   - Whitespace is trimmed, so gitignore's escaped trailing space and
//     backslash escapes (e.g. `foo\ `, `\#file`) are not preserved.
//   - An include ("!p") cannot re-include a path under a directory that an
//     earlier rule already excluded: rsync never descends into an excluded
//     directory, so `!build/keep` after `build/` has no effect. Reversing the
//     rule order cannot fix this — it is a structural difference from git.
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
