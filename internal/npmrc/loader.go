package npmrc

import (
	"os"
	"path/filepath"
	"strings"
)

// EnvPrefix is the only environment-variable prefix gnpm reads for
// configuration. npm_config_* / NPM_CONFIG_* are deliberately ignored
// (doc/spec.md §2.1) so another tool's ambient config cannot leak in.
const EnvPrefix = "GNPM_CONFIG_"

// Loader assembles the merged Config from the .npmrc layer stack. Fields
// left zero fall back to process defaults; tests inject them.
type Loader struct {
	// ProjectDir is where the project-level .npmrc search starts (walking
	// up to the filesystem root). Defaults to the current directory.
	ProjectDir string
	// HomeDir overrides the detected home for ~/.npmrc. Empty → detect.
	HomeDir string
	// GlobalConfig is the global npmrc path. Empty → /etc/npmrc.
	GlobalConfig string
	// Env overrides the process environment. nil → os.Environ.
	Env map[string]string
}

// Load reads and merges every available layer, returning the resolved
// Config. Precedence (highest wins): GNPM_CONFIG_* env, project .npmrc,
// user ~/.npmrc, global /etc/npmrc, with a separated auth file (when
// `npmrc-auth-file` is set) layered beneath the user-authored ones.
func (l Loader) Load() (*Config, error) {
	env := l.Env
	if env == nil {
		env = osEnvMap()
	}
	expand := makeExpander(env)

	global := l.GlobalConfig
	if global == "" {
		global = "/etc/npmrc"
	}
	home := l.HomeDir
	if home == "" {
		home = env["HOME"]
		if home == "" {
			home = env["USERPROFILE"]
		}
	}
	project := l.ProjectDir
	if project == "" {
		project, _ = os.Getwd()
	}

	// layers[0] is lowest precedence; later layers override earlier.
	var layers []map[string]string
	add := func(m map[string]string) {
		if m != nil {
			layers = append(layers, m)
		}
	}

	if m, err := readNpmrc(global, expand); err != nil {
		return nil, err
	} else {
		add(m)
	}
	if home != "" {
		if m, err := readNpmrc(filepath.Join(home, ".npmrc"), expand); err != nil {
			return nil, err
		} else {
			add(m)
		}
	}
	for _, dir := range walkUp(project) {
		m, err := readNpmrc(filepath.Join(dir, ".npmrc"), expand)
		if err != nil {
			return nil, err
		}
		if m != nil {
			add(m)
			break
		}
	}
	if envLayer := envOverrides(env); len(envLayer) > 0 {
		add(envLayer)
	}

	// Separated auth file: find the highest-precedence layer that sets
	// `npmrc-auth-file`, then insert that file beneath the user-authored
	// layers so explicit entries still override it.
	authFile := ""
	for i := len(layers) - 1; i >= 0; i-- {
		if v := layers[i]["npmrc-auth-file"]; v != "" {
			authFile = v
			break
		}
	}
	if authFile != "" {
		resolved := resolveAuthFile(authFile, project, home)
		if m, err := readNpmrc(resolved, expand); err != nil {
			return nil, err
		} else if m != nil {
			idx := 1
			if len(layers) <= 1 {
				idx = 0
			}
			layers = append(layers[:idx], append([]map[string]string{m}, layers[idx:]...)...)
		}
	}

	merged := map[string]string{}
	for _, layer := range layers {
		for k, v := range layer {
			merged[k] = v
		}
	}
	return New(merged), nil
}

func resolveAuthFile(raw, project, home string) string {
	if strings.HasPrefix(raw, "~/") && home != "" {
		return filepath.Join(home, raw[2:])
	}
	if filepath.IsAbs(raw) {
		return raw
	}
	return filepath.Clean(filepath.Join(project, raw))
}

func readNpmrc(path string, expand func(string) string) (map[string]string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	return ParseBody(string(data), expand), nil
}

// envOverrides extracts GNPM_CONFIG_* variables, mapping the
// UPPER_SNAKE suffix to the dash-separated lowercase .npmrc key so the
// same setting can be overridden from the environment.
func envOverrides(env map[string]string) map[string]string {
	out := map[string]string{}
	for k, v := range env {
		suffix, ok := cutPrefixFold(k, EnvPrefix)
		if !ok {
			continue
		}
		key := strings.ReplaceAll(strings.ToLower(suffix), "_", "-")
		out[key] = v
	}
	return out
}

func cutPrefixFold(s, prefix string) (string, bool) {
	if len(s) < len(prefix) {
		return "", false
	}
	if !strings.EqualFold(s[:len(prefix)], prefix) {
		return "", false
	}
	return s[len(prefix):], true
}

// makeExpander resolves ${VAR} (and npm's ${VAR-default}) against env.
func makeExpander(env map[string]string) func(string) string {
	return func(name string) string {
		base, def := name, ""
		if i := strings.IndexByte(name, '-'); i >= 0 {
			base, def = name[:i], name[i+1:]
		}
		if v, ok := env[base]; ok {
			return v
		}
		return def
	}
}

func walkUp(dir string) []string {
	abs, err := filepath.Abs(dir)
	if err != nil {
		abs = filepath.Clean(dir)
	}
	var out []string
	current := abs
	for {
		out = append(out, current)
		parent := filepath.Dir(current)
		if parent == current {
			break
		}
		current = parent
	}
	return out
}

func osEnvMap() map[string]string {
	out := map[string]string{}
	for _, kv := range os.Environ() {
		if i := strings.IndexByte(kv, '='); i >= 0 {
			out[kv[:i]] = kv[i+1:]
		}
	}
	return out
}
