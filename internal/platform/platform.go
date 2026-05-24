// Package platform provides OS/arch/libc detection in npm's vocabulary
// (for matching package `os`/`cpu`/`libc` fields) and the filesystem
// primitives the store and linker use: hardlink-with-copy-fallback,
// directory symlinks, and the executable-bit chmod.
package platform

import (
	"os"
	"runtime"
	"strings"
	"sync"
)

// OS returns the current operating system in npm's `process.platform`
// vocabulary (darwin / win32 / linux / …), used to match a package's
// `os` field.
func OS() string {
	switch runtime.GOOS {
	case "windows":
		return "win32"
	default:
		return runtime.GOOS // darwin, linux, android, freebsd, …
	}
}

// CPU returns the current architecture in npm's `process.arch`
// vocabulary (x64 / arm64 / ia32 / arm), used to match a package's `cpu`
// field.
func CPU() string {
	switch runtime.GOARCH {
	case "amd64":
		return "x64"
	case "386":
		return "ia32"
	case "arm64":
		return "arm64"
	case "arm":
		return "arm"
	default:
		return runtime.GOARCH
	}
}

var libcOnce struct {
	sync.Once
	value string
}

// Libc returns "musl" or "glibc" on Linux (probing for the musl loader),
// and "glibc" on every other platform, matching a package's `libc`
// field.
func Libc() string {
	libcOnce.Do(func() {
		libcOnce.value = detectLibc()
	})
	return libcOnce.value
}

func detectLibc() string {
	if runtime.GOOS != "linux" {
		return "glibc"
	}
	entries, err := os.ReadDir("/lib")
	if err != nil {
		return "glibc"
	}
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), "ld-musl-") {
			return "musl"
		}
	}
	return "glibc"
}

// MatchList reports whether current is accepted by an npm platform list
// such as `os`/`cpu`/`libc`. An empty list accepts everything. Entries
// prefixed with "!" are exclusions; when only exclusions are present,
// anything not excluded matches.
func MatchList(entries []string, current string) bool {
	if len(entries) == 0 {
		return true
	}
	var positives []string
	for _, e := range entries {
		if neg, ok := strings.CutPrefix(e, "!"); ok {
			if neg == current {
				return false
			}
			continue
		}
		positives = append(positives, e)
	}
	if len(positives) == 0 {
		return true
	}
	for _, p := range positives {
		if p == current {
			return true
		}
	}
	return false
}
