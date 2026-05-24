package regprovider

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/koji-1009/gnpm/internal/npmrc"
	"github.com/koji-1009/gnpm/internal/registry"
	"github.com/koji-1009/gnpm/internal/resolver"
	"github.com/koji-1009/gnpm/internal/semver"
)

// fakeRegistry serves packuments from an in-memory map.
func fakeRegistry(t *testing.T, packuments map[string]string) *registry.Client {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		name := r.URL.Path[1:] // strip leading /
		body, ok := packuments[name]
		if !ok {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		w.Write([]byte(body))
	}))
	t.Cleanup(srv.Close)
	return registry.NewClient(registry.Options{Config: npmrc.New(map[string]string{"registry": srv.URL + "/"})})
}

func TestProviderVersionsAndDeps(t *testing.T) {
	client := fakeRegistry(t, map[string]string{
		"demo": `{"name":"demo","dist-tags":{"latest":"1.2.0"},"versions":{
			"1.0.0":{"name":"demo","version":"1.0.0","dependencies":{"left-pad":"^1.0.0"}},
			"1.2.0":{"name":"demo","version":"1.2.0","dependencies":{"left-pad":"^1.0.0"}}
		}}`,
	})
	p := New(context.Background(), client, ReleaseAge{}, time.Time{})

	vers, err := p.Versions("demo")
	if err != nil {
		t.Fatal(err)
	}
	if len(vers) != 2 || vers[len(vers)-1].String() != "1.2.0" {
		t.Errorf("versions = %v", vers)
	}
	d, err := p.DependenciesOf("demo", semver.MustParse("1.2.0"))
	if err != nil {
		t.Fatal(err)
	}
	if d.Dependencies["left-pad"] != "^1.0.0" {
		t.Errorf("deps = %v", d.Dependencies)
	}
	tag, err := p.ResolveDistTag("demo", "latest")
	if err != nil || tag != "=1.2.0" {
		t.Errorf("dist-tag latest = %q (err=%v)", tag, err)
	}
}

func TestProviderReleaseAgeFilter(t *testing.T) {
	now := time.Date(2026, 5, 24, 0, 0, 0, 0, time.UTC)
	old := now.Add(-48 * time.Hour).Format(time.RFC3339)
	fresh := now.Add(-1 * time.Hour).Format(time.RFC3339)
	body := fmt.Sprintf(`{"name":"demo","dist-tags":{"latest":"2.0.0"},
		"time":{"1.0.0":%q,"2.0.0":%q},
		"versions":{"1.0.0":{"name":"demo","version":"1.0.0"},"2.0.0":{"name":"demo","version":"2.0.0"}}}`,
		old, fresh)
	client := fakeRegistry(t, map[string]string{"demo": body})

	// 24h minimum: the 1h-old 2.0.0 is filtered, only 1.0.0 remains.
	p := New(context.Background(), client, ReleaseAge{Minimum: 24 * time.Hour}, now)
	vers, err := p.Versions("demo")
	if err != nil {
		t.Fatal(err)
	}
	if len(vers) != 1 || vers[0].String() != "1.0.0" {
		t.Errorf("filtered versions = %v, want [1.0.0]", vers)
	}
}

func TestProviderDrivesResolver(t *testing.T) {
	client := fakeRegistry(t, map[string]string{
		"app": `{"name":"app","dist-tags":{"latest":"1.0.0"},"versions":{"1.0.0":{"name":"app","version":"1.0.0","dependencies":{"lib":"^2.0.0"}}}}`,
		"lib": `{"name":"lib","dist-tags":{"latest":"2.3.0"},"versions":{"2.0.0":{"name":"lib","version":"2.0.0"},"2.3.0":{"name":"lib","version":"2.3.0"}}}`,
	})
	p := New(context.Background(), client, ReleaseAge{}, time.Time{})
	res, err := resolver.NewSolver(resolver.Request{
		Dependencies:     map[string]string{"app": "^1.0.0"},
		Provider:         p,
		AutoInstallPeers: true,
	}).Solve()
	if err != nil {
		t.Fatal(err)
	}
	if res.Assignments["app"].String() != "1.0.0" || res.Assignments["lib"].String() != "2.3.0" {
		t.Errorf("resolved = %v", res.Assignments)
	}
}
