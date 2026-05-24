// Package project models a gnpm project on disk: its mode (which config
// and lockfile formats apply), its package.json, and dependency
// specifiers. See doc/spec.md §1–§3.
package project

import (
	"os"
	"path/filepath"
	"strings"
)

// Mode selects which config sources and lockfile format apply, decided
// purely from on-disk file presence (doc/spec.md §1).
type Mode int

const (
	// ModePnpm: pnpm artifacts present. Reads/writes pnpm-lock.yaml,
	// reads pnpm-workspace.yaml, treats .npmrc as auth-only.
	ModePnpm Mode = iota
	// ModeNpm: npm artifacts present (package-lock.json or a non-auth
	// .npmrc) and no pnpm artifacts. Reads package.json#gnpm, full
	// .npmrc; writes package-lock.json.
	ModeNpm
	// ModeGnpm: fresh project, no npm or pnpm artifacts. Same on-disk
	// behavior as ModeNpm.
	ModeGnpm
)

func (m Mode) String() string {
	switch m {
	case ModePnpm:
		return "pnpm"
	case ModeNpm:
		return "npm"
	default:
		return "gnpm"
	}
}

// LockfileName is the lockfile this mode reads and writes.
func (m Mode) LockfileName() string {
	if m == ModePnpm {
		return "pnpm-lock.yaml"
	}
	return "package-lock.json"
}

// DetectMode returns the project mode for root (doc/spec.md §1):
//  1. pnpm-workspace.yaml OR pnpm-lock.yaml present → pnpm
//  2. package-lock.json present OR .npmrc has a non-auth entry → npm
//  3. otherwise → gnpm
func DetectMode(root string) Mode {
	if fileExists(root, "pnpm-workspace.yaml") || fileExists(root, "pnpm-lock.yaml") {
		return ModePnpm
	}
	if fileExists(root, "package-lock.json") || hasNonAuthNpmrc(root) {
		return ModeNpm
	}
	return ModeGnpm
}

func fileExists(root, name string) bool {
	info, err := os.Stat(filepath.Join(root, name))
	return err == nil && !info.IsDir()
}

func hasNonAuthNpmrc(root string) bool {
	data, err := os.ReadFile(filepath.Join(root, ".npmrc"))
	if err != nil {
		return false
	}
	for _, line := range strings.Split(string(data), "\n") {
		stripped := stripCommentTrim(line)
		if stripped == "" {
			continue
		}
		eq := strings.IndexByte(stripped, '=')
		if eq < 0 {
			continue
		}
		key := strings.ToLower(strings.TrimSpace(stripped[:eq]))
		if isAuthKey(key) {
			continue
		}
		return true
	}
	return false
}

func stripCommentTrim(line string) string {
	inSingle, inDouble := false, false
	for i := 0; i < len(line); i++ {
		ch := line[i]
		if ch == '\'' && !inDouble {
			inSingle = !inSingle
		}
		if ch == '"' && !inSingle {
			inDouble = !inDouble
		}
		if !inSingle && !inDouble && (ch == '#' || ch == ';') {
			return strings.TrimSpace(line[:i])
		}
	}
	return strings.TrimSpace(line)
}

// isAuthKey reports whether an .npmrc key is credential-only and so does
// not count toward npm-mode detection (doc/spec.md §1).
func isAuthKey(key string) bool {
	if key == "_auth" || key == "email" {
		return true
	}
	return strings.Contains(key, ":_authtoken") ||
		strings.Contains(key, ":_password") ||
		strings.Contains(key, ":username") ||
		strings.Contains(key, ":_auth")
}
