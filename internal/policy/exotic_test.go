package policy

import "testing"

func TestIsTrustedExoticRepo(t *testing.T) {
	cases := []struct {
		url  string
		want bool
	}{
		// Trusted repos in the real specifier forms.
		{"github:nodejs/node", true},
		{"git+https://github.com/nodejs/node.git#v20", true},
		{"https://github.com/oven-sh/bun/archive/main.tar.gz", true},
		{"git://github.com/denoland/deno.git", true},

		// Untrusted repos.
		{"git+https://github.com/sindresorhus/got.git", false},
		{"github:expressjs/express", false},

		// Substring look-alikes that must NOT pass the trust gate:
		// a longer repo name, a deceptive host, or an owner suffix.
		{"https://evil.example/nodejs/node-malware.git", false},
		{"https://github.com/nodejs/node.attacker.com/x.tgz", false},
		{"https://github.com/evil-nodejs/node.git", false},
		{"https://github.com/oven-sh/bun-exploit.git", false},
	}
	for _, c := range cases {
		if got := IsTrustedExoticRepo(c.url); got != c.want {
			t.Errorf("IsTrustedExoticRepo(%q) = %v, want %v", c.url, got, c.want)
		}
	}
}
