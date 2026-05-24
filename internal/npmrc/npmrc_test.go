package npmrc

import (
	"net/url"
	"os"
	"path/filepath"
	"testing"
)

func TestParseBody(t *testing.T) {
	body := `
# a comment
registry=https://example.com/   ; trailing comment
@myorg:registry=https://npm.myorg.dev/
//npm.myorg.dev/:_authToken=secret
quoted="a value"
token=${MY_TOKEN}
withdefault=${MISSING-fallback}
bare-key
`
	env := map[string]string{"MY_TOKEN": "tok123"}
	got := ParseBody(body, makeExpander(env))

	want := map[string]string{
		"registry":                    "https://example.com/",
		"@myorg:registry":             "https://npm.myorg.dev/",
		"//npm.myorg.dev/:_authtoken": "secret",
		"quoted":                      "a value",
		"token":                       "tok123",
		"withdefault":                 "fallback",
	}
	for k, v := range want {
		if got[k] != v {
			t.Errorf("key %q = %q, want %q", k, got[k], v)
		}
	}
	if _, ok := got["bare-key"]; ok {
		t.Errorf("bare key without = should be skipped")
	}
}

func TestConfigAccessors(t *testing.T) {
	c := New(map[string]string{
		"registry":                   "https://r.example/",
		"@scope:registry":            "https://scope.example/",
		"//r.example/:_authtoken":    "tok",
		"//scope.example/:username":  "u",
		"//scope.example/:_password": "p",
		"named-registry-corp":        "https://corp.example/",
		"some-int":                   "42",
		"flag-true":                  "true",
		"flag-false":                 "false",
	})

	if c.Registry() != "https://r.example/" {
		t.Errorf("Registry() = %q", c.Registry())
	}
	if c.RegistryFor("@scope") != "https://scope.example/" {
		t.Errorf("RegistryFor(@scope) = %q", c.RegistryFor("@scope"))
	}
	if c.RegistryFor("scope") != "https://scope.example/" {
		t.Errorf("RegistryFor(scope) should normalize to @scope")
	}
	if c.Int("some-int", 0) != 42 {
		t.Errorf("Int(some-int) = %d", c.Int("some-int", 0))
	}
	if !c.Bool("flag-true", false) || c.Bool("flag-false", true) {
		t.Errorf("Bool parsing wrong")
	}

	if got := c.AuthTokenFor(mustURL(t, "https://r.example/")); got != "tok" {
		t.Errorf("AuthTokenFor = %q", got)
	}
	u, p, ok := c.BasicAuthFor(mustURL(t, "https://scope.example/"))
	if !ok || u != "u" || p != "p" {
		t.Errorf("BasicAuthFor = (%q,%q,%v)", u, p, ok)
	}

	nr := c.NamedRegistries()
	if nr["gh"] != BuiltinNamedRegistries["gh"] || nr["corp"] != "https://corp.example/" {
		t.Errorf("NamedRegistries = %v", nr)
	}
	if c.NamedRegistry("gh") == "" || c.NamedRegistry("corp") != "https://corp.example/" {
		t.Errorf("NamedRegistry lookups wrong")
	}
}

func TestLoaderPrecedenceAndEnv(t *testing.T) {
	home := t.TempDir()
	proj := t.TempDir()
	writeFile(t, filepath.Join(home, ".npmrc"), "registry=https://home.example/\nfoo=home\n")
	writeFile(t, filepath.Join(proj, ".npmrc"), "registry=https://project.example/\nbar=proj\n")

	env := map[string]string{
		"GNPM_CONFIG_REGISTRY":            "https://env.example/",
		"GNPM_CONFIG_MINIMUM_RELEASE_AGE": "1440",
		"NPM_CONFIG_REGISTRY":             "https://ignored.example/", // must be ignored
	}
	cfg, err := Loader{ProjectDir: proj, HomeDir: home, GlobalConfig: filepath.Join(t.TempDir(), "none"), Env: env}.Load()
	if err != nil {
		t.Fatal(err)
	}

	// env overrides project, which overrides home.
	if cfg.Registry() != "https://env.example/" {
		t.Errorf("registry = %q, want env override", cfg.Registry())
	}
	// UPPER_SNAKE env key maps to dash key.
	if cfg.Int("minimum-release-age", 0) != 1440 {
		t.Errorf("minimum-release-age = %d, want 1440", cfg.Int("minimum-release-age", 0))
	}
	if v, _ := cfg.Get("foo"); v != "home" {
		t.Errorf("foo = %q, want home", v)
	}
	if v, _ := cfg.Get("bar"); v != "proj" {
		t.Errorf("bar = %q, want proj", v)
	}
}

func TestLoaderAuthFileBeneathUserLayers(t *testing.T) {
	home := t.TempDir()
	proj := t.TempDir()
	authFile := filepath.Join(home, "auth.npmrc")
	writeFile(t, authFile, "//r.example/:_authToken=fromauthfile\nregistry=https://authfile.example/\n")
	writeFile(t, filepath.Join(proj, ".npmrc"), "npmrc-auth-file="+authFile+"\nregistry=https://project.example/\n")

	cfg, err := Loader{ProjectDir: proj, HomeDir: home, GlobalConfig: filepath.Join(t.TempDir(), "none"), Env: map[string]string{}}.Load()
	if err != nil {
		t.Fatal(err)
	}
	// Auth entry comes from the auth file...
	if got := cfg.AuthTokenFor(mustURL(t, "https://r.example/")); got != "fromauthfile" {
		t.Errorf("auth token = %q, want fromauthfile", got)
	}
	// ...but the explicit project registry still wins over the auth file's.
	if cfg.Registry() != "https://project.example/" {
		t.Errorf("registry = %q, want project to override auth file", cfg.Registry())
	}
}

func mustURL(t *testing.T, s string) *url.URL {
	t.Helper()
	u, err := url.Parse(s)
	if err != nil {
		t.Fatal(err)
	}
	return u
}

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}
