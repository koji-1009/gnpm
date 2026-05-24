// Package npmrc parses and merges .npmrc configuration across the layer
// precedence defined in doc/spec.md §2.1, and exposes registry / auth
// lookups over the merged result. The format is npm's own, so an
// existing project's .npmrc is consumed without translation.
package npmrc

import (
	"net/url"
	"regexp"
	"strconv"
	"strings"
)

// DefaultRegistry is the public npm registry.
const DefaultRegistry = "https://registry.npmjs.org/"

// BuiltinNamedRegistries are the named-registry aliases shipped by
// default. User .npmrc entries override these.
var BuiltinNamedRegistries = map[string]string{
	"gh": "https://npm.pkg.github.com/",
}

// Config is a merged, resolved .npmrc. Keys are stored lowercased, the
// npmrc convention.
type Config struct {
	entries map[string]string
}

// New wraps an entry map (already lowercased) as a Config.
func New(entries map[string]string) *Config {
	if entries == nil {
		entries = map[string]string{}
	}
	return &Config{entries: entries}
}

// Raw returns a copy of the merged entries.
func (c *Config) Raw() map[string]string {
	out := make(map[string]string, len(c.entries))
	for k, v := range c.entries {
		out[k] = v
	}
	return out
}

// Get returns the value for key (case-insensitive) and whether it was
// present.
func (c *Config) Get(key string) (string, bool) {
	v, ok := c.entries[strings.ToLower(key)]
	return v, ok
}

// GetOr returns the value for key or fallback when absent.
func (c *Config) GetOr(key, fallback string) string {
	if v, ok := c.Get(key); ok {
		return v
	}
	return fallback
}

// Int returns the integer value for key, or fallback when absent or
// unparseable.
func (c *Config) Int(key string, fallback int) int {
	v, ok := c.Get(key)
	if !ok {
		return fallback
	}
	n, err := strconv.Atoi(strings.TrimSpace(v))
	if err != nil {
		return fallback
	}
	return n
}

// Bool returns the boolean value for key, or fallback when absent. npm
// treats "true"/"false" and an empty value (bare key) as true.
func (c *Config) Bool(key string, fallback bool) bool {
	v, ok := c.Get(key)
	if !ok {
		return fallback
	}
	switch strings.ToLower(strings.TrimSpace(v)) {
	case "true", "":
		return true
	case "false":
		return false
	default:
		return fallback
	}
}

// Registry returns the default registry URL.
func (c *Config) Registry() string {
	return c.GetOr("registry", DefaultRegistry)
}

// RegistryFor returns the registry configured for a scope (e.g.
// "@myorg"), or "" when none is set.
func (c *Config) RegistryFor(scope string) string {
	return c.GetOr(normalizeScope(scope)+":registry", "")
}

// NamedRegistry resolves a named-registry alias to its URL, preferring a
// user override over the built-in. Returns "" for an unknown alias.
func (c *Config) NamedRegistry(alias string) string {
	if v, ok := c.Get("named-registry-" + strings.ToLower(alias)); ok {
		return v
	}
	return BuiltinNamedRegistries[strings.ToLower(alias)]
}

// NamedRegistries returns all aliases: built-ins unioned with user
// overrides (overrides win).
func (c *Config) NamedRegistries() map[string]string {
	out := make(map[string]string, len(BuiltinNamedRegistries))
	for k, v := range BuiltinNamedRegistries {
		out[k] = v
	}
	const prefix = "named-registry-"
	for k, v := range c.entries {
		if alias, ok := strings.CutPrefix(k, prefix); ok && alias != "" {
			out[alias] = v
		}
	}
	return out
}

// AuthTokenFor returns a bearer token configured for the registry URI,
// matching from the most specific path prefix down to the host.
func (c *Config) AuthTokenFor(registry *url.URL) string {
	for _, base := range registryKeyCandidates(registry) {
		if v, ok := c.entries[base+":_authtoken"]; ok {
			return v
		}
	}
	return ""
}

// BasicAuthFor returns the username/password pair configured for the
// registry URI, if both are present.
func (c *Config) BasicAuthFor(registry *url.URL) (username, password string, ok bool) {
	for _, base := range registryKeyCandidates(registry) {
		u, uok := c.entries[base+":username"]
		p, pok := c.entries[base+":_password"]
		if uok && pok {
			return u, p, true
		}
	}
	return "", "", false
}

// LegacyAuthFor returns a registry-wide `_auth` value (base64 of
// user:password), used by older Artifactory/Nexus deployments.
func (c *Config) LegacyAuthFor(registry *url.URL) string {
	for _, base := range registryKeyCandidates(registry) {
		if v, ok := c.entries[base+":_auth"]; ok {
			return v
		}
	}
	return c.entries["_auth"]
}

func normalizeScope(scope string) string {
	if strings.HasPrefix(scope, "@") {
		return scope
	}
	return "@" + scope
}

// registryKeyCandidates produces the `//host[:port]/path/` keys to probe
// for auth, from the most specific path prefix down to the bare host.
func registryKeyCandidates(u *url.URL) []string {
	hostPort := u.Host // already host[:port]
	var segs []string
	for _, s := range strings.Split(u.Path, "/") {
		if s != "" {
			segs = append(segs, s)
		}
	}
	var out []string
	for i := len(segs); i >= 0; i-- {
		pathPart := ""
		if len(segs) != 0 && i != 0 {
			pathPart = strings.Join(segs[:i], "/") + "/"
		}
		out = append(out, strings.ToLower("//"+hostPort+"/"+pathPart))
	}
	return out
}

// ParseBody parses one .npmrc file body into key/value pairs. expandVar
// resolves ${VAR} references (with npm's `${VAR-default}` form).
func ParseBody(content string, expandVar func(string) string) map[string]string {
	out := map[string]string{}
	for _, line := range strings.Split(content, "\n") {
		line = strings.TrimSuffix(line, "\r")
		stripped := strings.TrimSpace(stripComment(line))
		if stripped == "" {
			continue
		}
		eq := strings.IndexByte(stripped, '=')
		if eq < 0 {
			continue
		}
		key := strings.ToLower(strings.TrimSpace(stripped[:eq]))
		value := strings.TrimSpace(stripped[eq+1:])
		value = unquote(value)
		if expandVar != nil {
			value = expandVariables(value, expandVar)
		}
		out[key] = value
	}
	return out
}

func stripComment(line string) string {
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
			return line[:i]
		}
	}
	return line
}

func unquote(s string) string {
	if len(s) >= 2 {
		first, last := s[0], s[len(s)-1]
		if (first == '"' && last == '"') || (first == '\'' && last == '\'') {
			return s[1 : len(s)-1]
		}
	}
	return s
}

var varRefRe = regexp.MustCompile(`\$\{([^}]+)\}`)

func expandVariables(value string, lookup func(string) string) string {
	return varRefRe.ReplaceAllStringFunc(value, func(m string) string {
		body := m[2 : len(m)-1] // strip ${ and }
		return lookup(body)
	})
}
