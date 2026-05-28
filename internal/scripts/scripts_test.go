package scripts

import (
	"context"
	"runtime"
	"testing"
)

func TestBuildGate(t *testing.T) {
	triggers := BuildTriggers{HasPostinstall: true}
	none := BuildTriggers{}

	cases := []struct {
		name     string
		policy   BuildPolicy
		pkg      string
		triggers BuildTriggers
		want     BuildDecision
	}{
		{"no trigger", BuildPolicy{}, "x", none, BuildNoTrigger},
		{"allowlisted", BuildPolicy{AllowBuilds: []string{"esbuild"}}, "esbuild", triggers, BuildAllow},
		{"scope wildcard", BuildPolicy{AllowBuilds: []string{"@swc/*"}}, "@swc/core", triggers, BuildAllow},
		{"trailing glob", BuildPolicy{AllowBuilds: []string{"esb*"}}, "esbuild", triggers, BuildAllow},
		{"unreviewed non-strict", BuildPolicy{}, "evil", triggers, BuildSkip},
		{"unreviewed strict", BuildPolicy{StrictDepBuilds: true}, "evil", triggers, BuildFail},
		{"dangerous override", BuildPolicy{DangerouslyAllowAllBuilds: true}, "evil", triggers, BuildAllow},
		{"denylisted", BuildPolicy{NeverBuild: []string{"evil"}}, "evil", triggers, BuildSkip},
		{"denylist beats allowlist", BuildPolicy{AllowBuilds: []string{"evil"}, NeverBuild: []string{"evil"}}, "evil", triggers, BuildSkip},
		{"denylist beats dangerous", BuildPolicy{DangerouslyAllowAllBuilds: true, NeverBuild: []string{"evil"}}, "evil", triggers, BuildSkip},
		{"denylist glob", BuildPolicy{DangerouslyAllowAllBuilds: true, NeverBuild: []string{"ev*"}}, "evil", triggers, BuildSkip},
		{"denylist no trigger", BuildPolicy{NeverBuild: []string{"evil"}}, "evil", none, BuildNoTrigger},
	}
	for _, c := range cases {
		if got := c.policy.Evaluate(c.pkg, c.triggers); got != c.want {
			t.Errorf("%s: Evaluate = %v, want %v", c.name, got, c.want)
		}
	}
}

func TestTriggersFromScripts(t *testing.T) {
	tr := TriggersFromScripts(map[string]string{"postinstall": "node x", "prepare": "tsc"}, false, false)
	if !tr.HasPostinstall || !tr.Any() {
		t.Error("postinstall should be a trigger")
	}
	// prepare is not a trigger.
	tr2 := TriggersFromScripts(map[string]string{"prepare": "tsc"}, false, false)
	if tr2.Any() {
		t.Error("prepare alone must not be a trigger")
	}
}

func TestRunnerSuccessAndFailure(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX shell test")
	}
	r := NewRunner()
	res, err := r.Run(context.Background(), Script{
		Event: Postinstall, PackageName: "demo", PackageVersion: "1.0.0",
		WorkingDir: t.TempDir(), Command: "echo hi; exit 0",
	}, "")
	if err != nil || res.ExitCode != 0 {
		t.Fatalf("success script: err=%v code=%d", err, res.ExitCode)
	}

	_, err = r.Run(context.Background(), Script{
		Event: Install, PackageName: "demo", PackageVersion: "1.0.0",
		WorkingDir: t.TempDir(), Command: "exit 3",
	}, "")
	if err == nil {
		t.Fatal("expected error on non-zero exit")
	}
}

func TestBuildEnvRestricted(t *testing.T) {
	t.Setenv("HOME", "/home/test")
	t.Setenv("SECRET_TOKEN", "leak-me")
	env := BuildEnv(Script{Event: Install, PackageName: "demo", PackageVersion: "1.0.0"}, "/proj/node_modules/.bin", "/proj")
	got := map[string]string{}
	for _, kv := range env {
		for i := 0; i < len(kv); i++ {
			if kv[i] == '=' {
				got[kv[:i]] = kv[i+1:]
				break
			}
		}
	}
	if got["npm_package_name"] != "demo" || got["npm_lifecycle_event"] != "install" {
		t.Errorf("npm metadata missing: %v", got)
	}
	if got["INIT_CWD"] != "/proj" {
		t.Errorf("INIT_CWD = %q", got["INIT_CWD"])
	}
	if _, leaked := got["SECRET_TOKEN"]; leaked {
		t.Error("non-allowlisted env var leaked into lifecycle env")
	}
	if got["HOME"] != "/home/test" {
		t.Errorf("HOME should pass through: %q", got["HOME"])
	}
}
