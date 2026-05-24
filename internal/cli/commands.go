package cli

// commands is the dispatch table; the order here is the canonical listing
// order, while printUsage sorts by name for display.
var commands = []Command{
	{"install", "resolve, fetch, ingest, link; write lockfile + state", cmdInstall},
	{"ci", "locked install (= install --frozen-lockfile)", cmdCi},
	{"add", "add dependencies to package.json and install", cmdAdd},
	{"remove", "remove dependencies from package.json and install", cmdRemove},
	{"update", "bump dependencies within their declared ranges", cmdUpdate},
	{"list", "print the installed dependency tree", cmdList},
	{"why", "print reverse-dependency paths to a package", cmdWhy},
	{"outdated", "print packages with newer versions available", cmdOutdated},
	{"view", "print packument data", cmdView},
	{"pkg", "read or write fields in package.json", cmdPkg},
	{"run", "run a script from package.json#scripts", cmdRun},
	{"exec", "run a binary from node_modules/.bin", cmdExec},
	{"audit", "report known advisories for the dependency tree", cmdAudit},
	{"doctor", "print project mode, registry, and reachability", cmdDoctor},
	{"config", "read or write .npmrc entries", cmdConfig},
	{"clean", "remove node_modules; optionally the lockfile", cmdClean},
	{"peers", "check peer dependency satisfaction", cmdPeers},
	{"sbom", "emit a CycloneDX or SPDX software bill of materials", cmdSbom},
	{"dlx", "fetch a package into a cache and run its binary", cmdDlx},
}
