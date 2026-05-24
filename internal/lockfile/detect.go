package lockfile

import (
	"os"
	"path/filepath"

	"github.com/koji-1009/gnpm/internal/core"
	"github.com/koji-1009/gnpm/internal/npmrc"
	"github.com/koji-1009/gnpm/internal/project"
)

// ProjectLockfileName is the on-disk lockfile filename for a mode.
func ProjectLockfileName(mode project.Mode) string {
	return mode.LockfileName()
}

// Parse parses already-read lockfile bytes into the internal model,
// dispatching on mode. registry rebuilds the tarball URL pnpm leaves
// implicit for registry packages.
func Parse(data []byte, mode project.Mode, registry string) (*Lockfile, error) {
	if mode == project.ModePnpm {
		p, err := ParsePnpm(data)
		if err != nil {
			return nil, err
		}
		return PnpmToLockfile(p, registry), nil
	}
	return ImportNpm(data)
}

// Read reads the project's lockfile (format chosen by mode), returning
// (nil, nil) when none exists. registry is consulted only in pnpm mode.
func Read(projectRoot string, registry string) (*Lockfile, error) {
	mode := project.DetectMode(projectRoot)
	path := filepath.Join(projectRoot, ProjectLockfileName(mode))
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, core.LockfileError("reading lockfile %s: %v", path, err)
	}
	if registry == "" {
		registry = npmrc.DefaultRegistry
	}
	return Parse(data, mode, registry)
}

// Marshal serializes the lockfile to its on-disk bytes for the mode
// without writing, so callers can skip rewriting an unchanged file.
func Marshal(lock *Lockfile, projectName, projectVersion string, mode project.Mode) ([]byte, error) {
	if mode == project.ModePnpm {
		s, err := WritePnpmString(LockfileToPnpm(lock))
		return []byte(s), err
	}
	s, err := WriteNpmString(lock, projectName, projectVersion)
	return []byte(s), err
}

// Write writes the lockfile to the project (format chosen by mode),
// skipping the write when the serialized content is byte-identical to
// what is already on disk (so unchanged lockfiles don't churn). Returns
// the path and the bytes that represent the file's current content.
func Write(projectRoot string, lock *Lockfile, projectName, projectVersion string, mode project.Mode) (string, []byte, error) {
	path := filepath.Join(projectRoot, ProjectLockfileName(mode))
	body, err := Marshal(lock, projectName, projectVersion, mode)
	if err != nil {
		return "", nil, err
	}
	if existing, err := os.ReadFile(path); err == nil && bytesEqual(existing, body) {
		return path, existing, nil // unchanged — skip the write
	}
	if err := atomicWrite(path, body); err != nil {
		return "", nil, err
	}
	return path, body, nil
}

func bytesEqual(a, b []byte) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
