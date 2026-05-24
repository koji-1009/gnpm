package policy

import "strings"

// TrustedExoticRepos are the git/https repositories allowed as
// transitive exotic dependencies even when blockExoticSubdeps is on
// (doc/spec.md §2.4 blockExoticSubdeps).
var TrustedExoticRepos = []string{
	"nodejs/node",
	"oven-sh/bun",
	"denoland/deno",
}

// IsTrustedExoticRepo reports whether the specifier URL references one of
// the trusted "owner/repo" repositories (TrustedExoticRepos). The tree
// resolver calls this to gate a registry package's *transitive* exotic
// dependencies when blockExoticSubdeps is on; direct exotic dependencies
// are always allowed and bypass this check.
func IsTrustedExoticRepo(specifierURL string) bool {
	for _, repo := range TrustedExoticRepos {
		if containsRepoSegment(specifierURL, repo) {
			return true
		}
	}
	return false
}

// containsRepoSegment reports whether the "owner/repo" token appears in url
// as a whole path-segment pair: preceded by '/' or ':' (or the string
// start) and followed by a path boundary ('/', '#', ".git", or end). This
// keeps a hostname or a longer repo name that merely *contains* the token
// — e.g. "https://evil.example/nodejs/node-malware.git" — from passing the
// trust gate, which a plain substring match would wrongly allow.
func containsRepoSegment(url, repo string) bool {
	for from := 0; from <= len(url); {
		rel := strings.Index(url[from:], repo)
		if rel < 0 {
			return false
		}
		i := from + rel
		before := i == 0 || url[i-1] == '/' || url[i-1] == ':'
		rest := url[i+len(repo):]
		after := rest == "" || rest[0] == '/' || rest[0] == '#' || strings.HasPrefix(rest, ".git")
		if before && after {
			return true
		}
		from = i + 1
	}
	return false
}
