# gnpm

Normative reference for the gnpm package manager. Applies to gnpm `0.0.1-dev`.

gnpm is a Go CLI that installs npm packages. Its goal is **interoperation without a migration step**: point gnpm at an existing npm or pnpm project and it reads and writes that project's native formats directly, with no conversion pass. It reads and writes `package.json` + `package-lock.json` as its native at-rest formats and consumes pnpm artifacts (`pnpm-lock.yaml`, `pnpm-workspace.yaml`) transparently when a project is already on pnpm. This document specifies its commands, settings, file formats, and behavior. Anything not described here is unspecified.

gnpm does not publish packages. There is no `publish` command and no publish-time code path; the registry is read-only from gnpm's perspective.

This document is the contract. It describes **behavior**, not internal structure. [§11 Implementation notes](#11-implementation-notes-non-normative) is the only non-normative section and records the Go-specific design choices that make the behavior above efficient; it imposes no requirements.

---

## Contents

1. [Project modes](#1-project-modes)
2. [Configuration](#2-configuration)
3. [Dependency specifiers](#3-dependency-specifiers)
4. [Lockfiles](#4-lockfiles)
5. [CLI commands](#5-cli-commands)
6. [Lifecycle scripts](#6-lifecycle-scripts)
7. [Audit](#7-audit)
8. [Workspaces](#8-workspaces)
9. [Catalogs](#9-catalogs)
10. [dlx](#10-dlx)
11. [Implementation notes (non-normative)](#11-implementation-notes-non-normative)

---

## 1. Project modes

gnpm decides where it reads and writes configuration / lockfile data from the on-disk file presence at the project root, not from a flag.

| Mode | Trigger | Reads | Writes |
|---|---|---|---|
| `pnpm` | `pnpm-workspace.yaml` OR `pnpm-lock.yaml` is present | `pnpm-workspace.yaml`, `pnpm-lock.yaml`, auth-only entries from `.npmrc` | `pnpm-lock.yaml` |
| `npm` | `package-lock.json` is present, OR `.npmrc` contains a non-auth entry, AND `pnpm` mode does not match | `package.json#gnpm`, full `.npmrc`, `package-lock.json` | `package-lock.json` |
| `gnpm` | no file from either category above | same sources as `npm` mode | `package-lock.json` |

**Auth keys** that do not count as "non-auth" for `npm` mode detection (matched case-insensitively): `_auth`, `email`, and any key containing `:_authtoken`, `:_password`, `:username`, or `:_auth`.

The `gnpm` and `npm` modes share their on-disk surface; the distinction exists only to label projects that have never been touched by an npm-ecosystem tool. gnpm does not introduce a new lockfile format. The compatibility goal forbids it: a project must round-trip through gnpm and back to npm or pnpm without a format migration.

---

## 2. Configuration

### 2.1 Sources and precedence

Settings are read from the first source that defines them, highest priority first:

1. CLI flag (per-command, e.g. `--min-release-age=1440`)
2. Environment variable: `GNPM_CONFIG_<UPPER_SNAKE_KEY>=<value>`
3. Project `.npmrc` (walked from the project root up to the filesystem root; the first `.npmrc` found wins)
4. User `.npmrc` (`~/.npmrc`)
5. Global `.npmrc` (`/etc/npmrc`)
6. In `pnpm` mode, `pnpm-workspace.yaml` keys merge into the `.npmrc`-level entries.
7. In `npm` / `gnpm` mode, the `gnpm` object inside `package.json` is read for keys that the npm / pnpm `.npmrc` schema does not cover (`allowBuilds`, `auditConfig.ignoreGhsas`).

`GNPM_CONFIG_*` is the only environment-variable prefix gnpm reads. `npm_config_*` and `NPM_CONFIG_*` are deliberately ignored so an ambient configuration belonging to another package manager cannot silently override gnpm's behavior.

### 2.2 Auth-only `.npmrc` (`pnpm` mode)

In `pnpm` mode, `.npmrc` is treated as containing only credentials. Non-auth keys in `.npmrc` are read but do not influence behavior; configuration of policies lives in `pnpm-workspace.yaml`. This is so projects that share an `.npmrc` between pnpm and other tooling do not have their policy settings interpreted twice.

### 2.3 Separated auth file

```
# in .npmrc (any layer)
npmrc-auth-file=~/.gnpm/auth.npmrc
```

When `npmrc-auth-file` is set, gnpm reads the referenced file as an additional `.npmrc` layer inserted **beneath** every user-authored layer. Explicit entries in the project / user / global `.npmrc` still override the auth file. Paths starting with `~/` are resolved against the user's home directory; relative paths are resolved against the project root; absolute paths are used as-is.

The auth file is silently ignored if it does not exist.

### 2.4 Settings reference

Each setting below documents: **Type**, **Default**, **Source**, and **Behavior**. Examples are shown in the form they appear in the listed source.

#### `minimumReleaseAge`

- **Type**: non-negative integer (minutes)
- **Default**: when unset, **mode-dependent**: `1440` (one day) in pnpm mode, matching pnpm's own default; `0` (disabled) in npm mode, matching npm. An explicit `.npmrc` / flag value of `0` disables the filter in either mode.
- **Source**: `.npmrc minimum-release-age=` or CLI `--min-release-age=`

When the effective value is greater than `0`, a candidate version `v` of a package is filtered out of the resolver's candidate list when the registry's `time[v]` is more recent than `now - <value> minutes`. The filter requires the full packument (not the slim form); gnpm requests it automatically when the filter is on. An explicit `0` disables the filter. Leaving it unset applies the mode default above — so a plain `gnpm install` in a pnpm project is no less guarded against freshly published (and thus less-vetted) releases than pnpm. Unit suffixes (`24h`, `7d`, etc.) are rejected — pnpm's `minimumReleaseAge` is plain minutes, and accepting suffixes would make the same `pnpm-workspace.yaml` value behave differently under gnpm.

```
# .npmrc — wait 1 day before installing a newly published version
minimum-release-age=1440
```

#### `minimumReleaseAgeStrict`

- **Type**: boolean
- **Default**: `false`
- **Source**: `.npmrc minimum-release-age-strict=`

When every version of a needed package is younger than the `minimumReleaseAge` cutoff: `true` fails the install with a network error. `false` falls back to the lowest-versioned otherwise-filtered candidate so installs do not stall on a freshly published package.

#### `minimumReleaseAgeIgnoreMissingTime`

- **Type**: boolean
- **Default**: `true`
- **Source**: `.npmrc minimum-release-age-ignore-missing-time=`

How to treat a candidate version whose packument has no `time[v]` entry. `true` admits it (some legacy / mirrored registries omit `time`); `false` rejects it as "too new to verify".

#### `minimumReleaseAgeExclude`

- **Type**: list of patterns (`name`, `@scope/name`, `@scope/*`)
- **Default**: empty
- **Source**: `.npmrc minimum-release-age-exclude=` (comma-separated)

Packages matching any pattern bypass the `minimumReleaseAge` filter.

#### `allowBuilds`

- **Type**: list of patterns (`name`, `name*`, `@scope/*`)
- **Default**: empty
- **Source**:
  - `npm` / `gnpm` mode: `package.json#gnpm.allowBuilds`
  - `pnpm` mode: `pnpm-workspace.yaml#allowBuilds`

A reviewed allowlist of packages whose install-time scripts may run. See [§6 Lifecycle scripts](#6-lifecycle-scripts) for the gate. The pnpm key `package.json#onlyBuiltDependencies` is also accepted and unioned with `allowBuilds`, so a pnpm project's existing allowlist is honored without editing.

```jsonc
// package.json
{
  "gnpm": {
    "allowBuilds": ["esbuild", "@swc/*"]
  }
}
```

#### `strictDepBuilds`

- **Type**: boolean
- **Default**: `false`
- **Source**: `.npmrc strict-dep-builds=`

`true` upgrades an unreviewed build-script-bearing dependency from a silent skip-with-warning to a hard install failure. pnpm v11 ships `true` by default; gnpm keeps `false` so existing pnpm projects can migrate without re-running an install audit.

#### `dangerouslyAllowAllBuilds`

- **Type**: boolean
- **Default**: `false`
- **Source**: `.npmrc dangerously-allow-all-builds=` or CLI `--allow-scripts=all`

Skips the [build-script gate](#6-lifecycle-scripts) entirely. Every package's `preinstall` / `install` / `postinstall` runs. The flag is named to discourage casual use.

#### `blockExoticSubdeps`

- **Type**: boolean
- **Default**: `false`
- **Source**: `.npmrc block-exotic-subdeps=` or `pnpm-workspace.yaml#blockExoticSubdeps`

When `true`, a **transitive** dependency declared via a git or https-tarball specifier is rejected unless its repository is on the `trustedExoticRepos` allowlist (currently `nodejs/node`, `oven-sh/bun`, `denoland/deno`). A **direct** exotic dependency declared in the root `package.json` is always allowed; the project author opted in explicitly.

#### `trustPolicy`

- **Type**: `off` | `no-downgrade`
- **Default**: `off`
- **Source**: `.npmrc trust-policy=` or `pnpm-workspace.yaml#trustPolicy`

`no-downgrade` refuses to resolve to a version below the highest version the project has previously installed for the same package. History is persisted alongside the lockfile under `node_modules/.gnpm/`. Defends against republish-based downgrade attacks.

#### `trustPolicyIgnoreAfter`

- **Type**: duration
- **Default**: unset (history kept forever)
- **Source**: `.npmrc trust-policy-ignore-after=`

Drop trust records older than this duration when evaluating `trustPolicy=no-downgrade`.

#### `signaturePolicy`

- **Type**: `none` | `weak` | `strict`
- **Default**: `none`
- **Source**: CLI `--enforce-signatures=`

Registry-attached ECDSA P-256 signatures over `<name>@<version>:<integrity>` are checked against the registry's published public key (`/-/npm/v1/keys`).

- `none`: signatures are not verified.
- `weak`: when a signature is present it must verify; absence is allowed.
- `strict`: every tarball must carry a valid signature.

Signatures authenticate the registry, not the publisher.

#### `pmOnFail`

- **Type**: `ignore` | `warn` | `error`
- **Default**: `warn`
- **Source**: `.npmrc pm-on-fail=` or `pnpm-workspace.yaml#pmOnFail`

What gnpm does when `package.json#packageManager` or `package.json#devEngines.packageManager` pins a manager and version range that this gnpm binary does not satisfy.

| Value | Behavior |
|---|---|
| `ignore` | proceed |
| `warn` | emit a warning, proceed |
| `error` | fail the install |

`devEngines.packageManager.onFail`, when present, overrides this setting for the project where it appears.

#### `optimisticRepeatInstall`

- **Type**: boolean
- **Default**: `true`
- **Source**: gnpm internal default (no public flag)

When `true`, an `install` invocation hashes the project's `(package.json deps, package-lock.json bytes, engine key)` and compares to `node_modules/.gnpm/workspace-state.json`. On match, install returns successfully without running the resolver or touching the store. Suppressed when `--frozen-lockfile` is in effect.

#### `verifyDepsBeforeRun`

- **Type**: `off` | `warn` | `error` | `install` | `prompt`
- **Default**: `install`
- **Source**: `.npmrc verify-deps-before-run=` or `pnpm-workspace.yaml#verifyDepsBeforeRun`

Run by `gnpm run` / `gnpm exec` before invoking the requested script / binary. Computes the same hash as `optimisticRepeatInstall` and, on mismatch:

| Value | Behavior |
|---|---|
| `off` | proceed without checking |
| `warn` | print a warning, proceed |
| `error` | abort |
| `install` | run a fresh `gnpm install` first, then proceed |
| `prompt` | ask interactively (TTY only; non-TTY uses `error` semantics) |

#### `catalogMode`

- **Type**: `manual` | `strict` | `prefer`
- **Default**: `manual`
- **Source**: `.npmrc catalog-mode=` or `pnpm-workspace.yaml#catalogMode`

Governs how catalogs interact with workspace `package.json` ranges. See [§9 Catalogs](#9-catalogs).

#### `namedRegistries`

- **Type**: map `<alias>` → URL
- **Default**: `{ gh: "https://npm.pkg.github.com/" }`
- **Source**: `.npmrc named-registry-<alias>=<url>` (one line per alias) or `pnpm-workspace.yaml#namedRegistries`

Defines short aliases usable in registry selectors and dependency URLs. User entries are unioned with the built-in `gh:` alias; user values override the built-in for the same alias name.

#### `configDependencies`

- **Type**: map (`name` → `version` shorthand, or `name` → `{version, integrity}`)
- **Default**: empty
- **Source**: `package.json#configDependencies` (npm / gnpm mode) or `pnpm-workspace.yaml#configDependencies` (pnpm mode)

Declares packages that ship shared configuration (lint rules, prettier config, etc.) to be materialized under `node_modules/.gnpm-config/<name>/` rather than the standard `node_modules/`.

### 2.5 Lifecycle script environment

Lifecycle scripts (run by `gnpm install` / `gnpm run`) do not receive a copy of the host environment. gnpm forwards the following variables from the process environment when set, plus `PATH` (with the project's `node_modules/.bin` prepended) and the npm-style metadata variables:

| Forwarded | Set per-script |
|---|---|
| `HOME`, `USER`, `LOGNAME`, `SHELL`, `TERM`, `PWD`, `LANG`, `LC_ALL`, `LC_CTYPE`, `LC_MESSAGES`, `TMPDIR` (POSIX) | `npm_lifecycle_event` |
| `USERPROFILE`, `USERNAME`, `COMPUTERNAME`, `SYSTEMROOT`, `WINDIR`, `TEMP`, `TMP` (Windows) | `npm_package_name` |
| `NODE_OPTIONS`, `CI` | `npm_package_version` |
| `PATH` (with `node_modules/.bin` prepended) | `INIT_CWD` |

The metadata variables use the `npm_` prefix, not a gnpm-specific one, because installed packages' scripts expect exactly those names. `npm_package_json` is not set even when present in the host environment. Any other variable is dropped.

---

## 3. Dependency specifiers

A value in `dependencies` / `devDependencies` / `optionalDependencies` / `peerDependencies` matches one of the following forms.

| Form | Example | Meaning |
|---|---|---|
| Semver range | `^1.2.0`, `>=2 <3`, `1.2.3` | Resolve via the configured registry against the named package's published versions. |
| Alias | `npm:react-native@^0.74.0` | Same as semver, but the resolved package's name is overridden by the alias prefix. |
| Workspace | `workspace:^`, `workspace:*` | Link to a workspace package (see [§8 Workspaces](#8-workspaces)). |
| File | `file:../shared-lib` | Pack the local directory into the store and link from there. |
| Link | `link:../shared-lib` | Create a direct symlink (no store copy). |
| HTTPS tarball | `https://example.com/pkg.tgz` | Download and ingest. |
| Git | `git+https://github.com/owner/repo.git#ref`, `github:owner/repo#ref` | Clone and pack. |
| Catalog | `catalog:`, `catalog:testing` | Resolve via the named catalog table (see [§9 Catalogs](#9-catalogs)). |

---

## 4. Lockfiles

### 4.1 `package-lock.json` (npm v3 shape)

Read and written in `npm` / `gnpm` mode. gnpm preserves two extensions on each package entry:

- `_signatures`: list of `{keyid, sig}` pairs captured at resolution time, used by [`signaturePolicy`](#signaturepolicy) to verify warm installs against the registry's signing key.
- `_scripts`: a copy of the package's `scripts` map for entries whose packument-slim form omits the bodies. Used so a warm install knows what to run without re-fetching the manifest.

Both fields begin with `_`, which npm reserves for and ignores on unknown keys; lockfiles round-trip through npm without breaking interoperability. This is a hard requirement of the compatibility goal: `npm ci` against a gnpm-written lockfile must succeed, and a gnpm install against an npm-written lockfile must reproduce the same tree.

### 4.2 `pnpm-lock.yaml`

Read in `pnpm` mode. The reader preserves every unknown top-level key so a subsequent write does not silently drop unfamiliar fields.

The writer produces block-style YAML. Output is **semantic-equivalent to input**, not byte-equivalent: comments, blank lines, and key ordering inside a map may change. Workflows that rely on stable diffs across pnpm-to-gnpm round trips are unsupported. A pnpm-written lockfile must remain installable by pnpm after gnpm rewrites it, and vice versa; byte-stable diffs are not part of that contract.

Recognized top-level sections: `lockfileVersion`, `settings`, `importers`, `packages`, `snapshots`, `catalogs`. Settings inside `settings` are interpreted as if they were in `pnpm-workspace.yaml`.

### 4.3 Workspace state

`<projectRoot>/node_modules/.gnpm/workspace-state.json` is written after every successful install. Schema:

```json
{
  "schemaVersion": 1,
  "hash": "<sha256 hex>",
  "engineKey": "<platform>;<arch>;node<major>",
  "installedAt": "<RFC 3339 UTC>",
  "gnpmVersion": "<semver>"
}
```

This file is gnpm-private. It is neither an npm nor a pnpm artifact, so its only requirement is internal self-consistency: the same project state must always produce the same `hash`. It does not need to match any other tool's hash, and a reimplementation only has to agree with *itself* across runs.

`engineKey`'s `<major>` is the major version pinned by `devEngines.runtime` when present, the value of `GNPM_HOST_NODE_MAJOR` otherwise, and `?` when neither is set. gnpm does not invoke `node` to detect a host major.

Consumers: [`optimisticRepeatInstall`](#optimisticrepeatinstall) and [`verifyDepsBeforeRun`](#verifydepsbeforerun).

#### 4.3.1 `hash` canonicalization

The `hash` field is the SHA-256, as 64 lowercase hex characters, of the canonical JSON encoding of an object built as follows. The procedure is fully specified so any build of gnpm produces the same `hash` for the same inputs.

1. **Build the input object** with these six keys in this exact order (the order is fixed and is **not** alphabetical):

    1. `dependencies` — the project's `package.json#dependencies` (`{}` when absent).
    2. `devDependencies` — `package.json#devDependencies` (`{}` when absent).
    3. `optionalDependencies` — `package.json#optionalDependencies` (`{}` when absent).
    4. `peerDependencies` — `package.json#peerDependencies` (`{}` when absent).
    5. `lockfile` — the lockfile fingerprint string (see step 2).
    6. `engineKey` — the engine key string `"<platform>;<arch>;node<major>"`.

2. **Lockfile fingerprint.** The value at `lockfile` is:

    - When no lockfile path is supplied, or the path does not refer to an existing readable file, the literal string `"absent"`.
    - Otherwise the lockfile's raw bytes are read **without UTF-8 normalization, line-ending normalization, or trimming**, hashed with SHA-256, and the value becomes `"sha256:"` followed by the digest as 64 lowercase hex characters.

3. **Key ordering.** The top-level object's keys are emitted in the fixed order of step 1. Within each of the four dependency maps, keys are emitted in ascending byte order of their UTF-8 encoding — the default order Go's `encoding/json` applies to map keys. Each value is the dependency specifier string verbatim.

4. **Canonical JSON.** Serialize with these properties (Go's `encoding/json` with HTML escaping disabled is the reference encoder):

    - **No insignificant whitespace** between tokens, **no trailing newline**, **no byte-order mark**.
    - **String escaping**: `"`, `\`, and the C0 control characters `U+0000`…`U+001F` are escaped (`\"`, `\\`, `\b`, `\t`, `\n`, `\f`, `\r`, and `\u00xx` with lowercase hex for the remaining controls). The characters `<`, `>`, `&` are **not** escaped — Go's default `Marshal` would emit them as `<` / `>` / `&`, so the encoder must run with HTML escaping turned off. All other printable code points, including non-ASCII, appear as their literal UTF-8 bytes.
    - **Output encoding** is UTF-8; the bytes fed to SHA-256 are exactly those UTF-8 bytes.
    - Every value in the object is either a string or a (possibly empty) map of strings to strings. No numbers or booleans appear.

5. **Hash and format.** SHA-256 of the UTF-8 byte sequence from step 4, formatted as 64 lowercase hex characters.

Reference example for an empty project (no dependencies of any kind, no lockfile, engine key `"linux;amd64;node?"`):

```
canonical = {"dependencies":{},"devDependencies":{},"optionalDependencies":{},"peerDependencies":{},"lockfile":"absent","engineKey":"linux;amd64;node?"}
hash      = sha256(utf8(canonical))  // lowercase hex
```

> Note: `<platform>` and `<arch>` use Go's `runtime.GOOS` / `runtime.GOARCH` vocabulary (e.g. `darwin`, `linux`, `windows`; `amd64`, `arm64`). This differs from npm's `process.platform` / `process.arch` vocabulary (`win32`, `x64`, `ia32`) used elsewhere for package `os` / `cpu` matching — the engine key is a gnpm-private value and is not interpreted by any other tool.

---

## 5. CLI commands

Common conventions:

- Global flags: `--silent`, `--verbose` (`-v`), `--loglevel <silent|error|warn|info|debug|trace>`, `--color`, `--version`, `--json`.
- Exit-code semantics are listed in [§5.1 Exit codes](#51-exit-codes).

| Command | Synopsis | Behavior |
|---|---|---|
| `install` | `gnpm install [--frozen-lockfile] [--ignore-scripts] [--allow-scripts=<none\|allowlist\|all>] [--min-release-age=<minutes>] [--enforce-signatures=<none\|weak\|strict>] [--audit-level=<low\|moderate\|high\|critical>] [--offline] [--prefer-offline] [--production] [--engine-strict]` | Resolve, fetch, ingest, link. Writes lockfile + workspace state. |
| `ci` | `gnpm ci` | Locked install. Equivalent to `install --frozen-lockfile`; aborts when the lockfile and `package.json` diverge. |
| `add` | `gnpm add <pkg>[@<spec>]...` | Add dependencies to `package.json` and install. |
| `remove` | `gnpm remove <pkg>...` | Remove from `package.json` and install. |
| `update` | `gnpm update [<pkg>...]` | Bump within declared ranges. |
| `list` | `gnpm list [<pkg>]` | Print the installed dependency tree. |
| `why` | `gnpm why <pkg>` | Print reverse-dependency paths leading to a package. |
| `outdated` | `gnpm outdated` | Print packages with newer versions available. |
| `view` | `gnpm view <pkg>[@<spec>] [<field>]` | Print packument data. |
| `pkg` | `gnpm pkg {get,set} <path> [<value>]` | Read or write fields in `package.json`. |
| `run` | `gnpm run <script> [-- <args...>]` | Run a script entry from `package.json#scripts`. Honors [`verifyDepsBeforeRun`](#verifydepsbeforerun). The script's exit code is propagated. |
| `exec` | `gnpm exec <bin> [<args...>]` | Run a binary from `node_modules/.bin`. The bin's exit code is propagated. |
| `audit` | `gnpm audit [--ignore-ghsas=<csv>] [--level=<sev>] [--json]` | See [§7 Audit](#7-audit). |
| `doctor` | `gnpm doctor` | Print project mode, resolved registry, named registries, registry reachability. |
| `config` | `gnpm config {get,set,delete} <key> [<value>]` | Read or write `.npmrc` entries. |
| `clean` | `gnpm clean [--delete-lockfile] [--dry-run]` | Remove `node_modules/`; optionally remove the lockfile. |
| `peers` | `gnpm peers check` | Print unsatisfied or mis-versioned peer dependencies. |
| `sbom` | `gnpm sbom [--format=<cyclonedx\|spdx>] [-o <file>]` | Emit CycloneDX 1.7 or SPDX 2.3 JSON. |
| `dlx` | `gnpm dlx [-p <pkg>...] [-c <bin>] [--offline] <pkg> [<args...>]` | See [§10 dlx](#10-dlx). The bin's exit code is propagated. |

### 5.1 Exit codes

gnpm uses three primary exit codes (`sysexits.h`-derived: `0`, `64`, `70`) plus `1` for command-specific "recoverable" outcomes. Commands that spawn a child process (`run`, `exec`, `dlx`) propagate the child's exit code verbatim and may therefore exit with any value the child returns.

| Code | Condition |
|---|---|
| `0` | The command completed successfully. |
| `1` | `audit` found at least one advisory whose severity is ≥ `--audit-level` (default `high`). |
| `1` | `audit` produced no findings but at least one advisory fetch from the registry failed; the partial result is treated as failure so CI does not pass on incomplete data. |
| `1` | `peers check` found at least one unsatisfied or mismatched non-optional peer dependency. |
| `1` | `peers check` could not locate a project lockfile. |
| `1` | `audit` was invoked without a project lockfile. |
| `1` | `why` was given a package that is not present in the resolved dependency graph. |
| `1` | `clean` failed to delete a target path because of a filesystem error. |
| `1` | `doctor` detected at least one diagnostic failure (`node` is not on `PATH`, or the resolved registry is unreachable). |
| `64` | The argument parser rejected the invocation: unknown command, unknown flag, missing required value, or an option value outside the declared allowed set. |
| `64` | A required positional argument is missing or malformed. This covers `add` / `remove` with no package names, `why` / `view` with no package name, `run` with no script name, `exec` with no binary, `list` / `why` invoked without a project lockfile, `pkg get` / `pkg set` / `pkg delete` with no field path, `pkg set` with a value not in `path=value` form, `config get` / `config delete` with no key, `config set` with fewer than two arguments, `dlx` with no package, and `dlx` with a malformed scoped package specifier. |
| `64` | `sbom` was invoked without `--format`, or without a project lockfile. |
| `64` | `run` was invoked with a script name that is not present in `package.json#scripts`. |
| `64` | `exec` was invoked with a binary that is not present in `node_modules/.bin`. |
| `64` | `dlx` could not locate the user's home directory for the cache root. |
| `64` | `dlx` finished installing the requested package(s) but the requested bin is not present after install; pass `--call` to disambiguate. |
| `64` | `install` or `ci` was given an unrecognized value for `--allow-scripts`, `--enforce-signatures`, or `--min-release-age`. |
| `64` | `install` or `ci` refused to run a dependency's install-time scripts because the package is not on `allowBuilds` and [`strictDepBuilds`](#strictdepbuilds) is `true`. |
| `64` | `install` or `ci` ran under [`pmOnFail`](#pmonfail) `error` and the host gnpm binary does not satisfy `package.json#packageManager` or `package.json#devEngines.packageManager`. |
| `64` | `install --frozen-lockfile` (or `ci`) found that resolution diverges from `package-lock.json`. |
| `64` | `install --engine-strict` (or `ci`) found that a dependency's `engines.node` is incompatible with the running node. |
| `70` | Any other failure raised as a typed gnpm error: `NetworkError` (DNS / TLS / connect / timeout / non-2xx HTTP), `IntegrityError` (tarball hash mismatch, signature verification failure, or post-install `--audit-level` finding raised from `install`), `ResolutionError` (solver could not find an assignment), `IoError` (permission, `ENOSPC`, `EXDEV`, etc.), `LockfileError` (lockfile parse or round-trip), `ScriptError` (lifecycle script that surfaced as a hard failure), or `CancelledError` (the command's context was cancelled). |
| `70` | Any unhandled panic that escapes the command runner. The stack trace is written to stderr. |
| `<child>` | `run`, `exec`, and `dlx` propagate the spawned process's exit code unchanged. |

The `64` and `70` values match `EX_USAGE` and `EX_SOFTWARE` from `sysexits.h`. The `1` value matches the convention used by `npm` and `pnpm` for audit and peer-check failures so existing CI scripts continue to work without re-coding the success / failure split.

### 5.2 JSON output (`--json`)

The global `--json` flag switches a command from its human-oriented output to a machine-readable JSON document on stdout. Diagnostics (warnings, fetch errors) continue to be written to stderr in their normal text form.

Only `audit` reads the global `--json` flag. `view`, `pkg`, and `sbom` emit JSON unconditionally because their entire purpose is to produce machine-readable data; the `--json` flag is a no-op for them, and they are listed here so their on-the-wire shape is part of the spec. Every other command ignores `--json` and prints the same human-oriented output it would without the flag.

#### `audit` (`--json`)

Top-level shape: a single JSON object. Always emitted on stdout, even when there are no findings.

```
{
  "findings": [
    {
      "package":             "<string>",          // e.g. "lodash"
      "installed":           "<string>",          // installed version
      "severity":            "<string>",          // "info" | "low" | "moderate" | "high" | "critical"
      "title":               "<string>",
      "vulnerable_versions": "<string>",          // npm range string
      "patched_versions":    "<string> | null",   // npm range string, or null when no fix is known
      "url":                 "<string>",          // advisory URL
      "id":                  "<string>"           // GHSA ID
    }
  ],
  "totals": {
    "<severity>": <integer>                       // one entry per severity that has at least one finding
  },
  "total":  <integer>,                            // sum of `totals`
  "errors": [ "<string>", ... ]                   // per-registry advisory fetch errors
}
```

`findings` is not deduplicated: the same `(package, id)` pair may appear once per installed version, ordered as encountered by the auditor. Only `totals` keys with a non-zero count are present; an empty audit emits `"totals": {}`. The `--fix` plan is not represented in JSON; `gnpm audit --fix --json` still prints the plan in text form on stdout below the JSON object.

#### `view`

Top-level shape depends on the invocation:

- `gnpm view <pkg>` (no field): a JSON object.

  ```
  {
    "name":      "<string>",
    "dist-tags": { "<tag>": "<version>", ... },   // e.g. { "latest": "1.2.3" }
    "versions":  [ "<version>", ... ]             // every published version, registry order
  }
  ```

- `gnpm view <pkg> <field>` (one field): a bare JSON value whose type depends on `<field>`:
  - `name` → string
  - `versions` → array of strings
  - `dist-tags` → object of `<tag>` → `<version>`
  - `latest` → string (or `null` if the packument has no `latest` dist-tag)
  - any other field → `null`

The pretty-printed form (two-space indent) is what is written to stdout.

#### `pkg`

`gnpm pkg get <field>...` writes JSON to stdout:

- One `<field>`: the bare JSON value at that path in `package.json`, or `null` when the path does not exist. Compact (no indent).
- Two or more `<field>`s: a JSON object keyed by the supplied field paths, each value being the resolved value or `null`. Pretty-printed with two-space indent.

`gnpm pkg set` and `gnpm pkg delete` mutate `package.json` and produce no stdout output.

#### `sbom`

Top-level shape is fixed by the chosen format. `--format` is required; the global `--json` flag has no effect.

- `--format=cyclonedx` emits a CycloneDX 1.7 JSON document. Top-level keys: `bomFormat`, `specVersion`, `serialNumber`, `version`, `metadata`, `components`. `serialNumber` is `urn:gnpm:sbom:<sha256 hex>` derived from the sorted set of `name@version|integrity` tuples in the lockfile and is therefore stable across re-runs over an unchanged lockfile.
- `--format=spdx` emits an SPDX 2.3 JSON document. Top-level keys: `spdxVersion`, `dataLicense`, `SPDXID`, `name`, `documentNamespace`, `creationInfo`, `packages`.

Each `components[]` / `packages[]` entry populates `name`, `version`, `purl` (npm purl), and `hashes` / `checksums` from the lockfile's integrity field when present. The detailed shape of each entry is delegated to the CycloneDX 1.7 and SPDX 2.3 specifications.

#### Stability

The `--json` schema is **not yet a stable contract for 0.x releases**. Field names may be added, removed, or renamed in a `0.x → 0.x+1` transition without a deprecation period. Tooling that consumes gnpm's JSON output should pin to a specific gnpm version. The `1.0` release will freeze the schemas of every `--json`-supporting command listed in this section; later changes will be additive only, or staged through a documented deprecation.

The third-party SBOM document shapes (CycloneDX 1.7, SPDX 2.3) are governed by their respective external specifications, not by gnpm, and are excluded from the `1.0` freeze.

---

## 6. Lifecycle scripts

gnpm runs three classes of lifecycle scripts:

- **Root preinstall** — `package.json#scripts.preinstall` of the project itself, before any resolution.
- **Per-dependency install-time** — `preinstall`, `install`, `postinstall` declared by an installed dependency.
- **Per-dependency prepare** — `prepare` declared by an installed dependency, after `install` / `postinstall`.

### 6.1 Build-script gate

A dependency is **build-script-bearing** if any of the following is true:

- `scripts.preinstall` is non-empty
- `scripts.install` is non-empty
- `scripts.postinstall` is non-empty
- a `binding.gyp` file exists at the package root
- a `.hooks/` directory exists at the package root

`prepare` is not a trigger.

Given the `scriptPolicy` (CLI `--allow-scripts`, default `allowlist`):

| `scriptPolicy` | `dangerouslyAllowAllBuilds` | Trigger? | In `allowBuilds`? | Action |
|---|---|---|---|---|
| `none` | — | — | — | skip all install-time scripts |
| `all` | — | — | — | run |
| `allowlist` | `true` | — | — | run |
| `allowlist` | `false` | no | — | run (no trigger to gate) |
| `allowlist` | `false` | yes | yes | run |
| `allowlist` | `false` + `strictDepBuilds=false` | yes | no | skip, emit warning |
| `allowlist` | `false` + `strictDepBuilds=true` | yes | no | fail the install |

### 6.2 Pattern matching for `allowBuilds`

A pattern matches the package's logical name if it is:

- An exact equality (`react` matches `react`).
- A scope wildcard ending `/*` (`@types/*` matches every package inside the `@types` scope).
- A trailing wildcard ending `*` (`@babel/preset-*` matches `@babel/preset-env`).

No other glob syntax is supported.

### 6.3 Script execution environment

See [§2.5 Lifecycle script environment](#25-lifecycle-script-environment).

A script is run via `/bin/sh -c <command>` on POSIX and `cmd.exe /C <command>` on Windows. Timeout defaults to 10 minutes; a script exceeding it has its process group killed.

---

## 7. Audit

`gnpm audit` posts a bulk advisory request to `<registry>/-/npm/v1/security/advisories/bulk` for every distinct `name@version` in the lockfile. Scope-specific registries are honored, so audits split across multiple registries when scopes are configured.

### 7.1 Ignore list

The set of GHSA IDs excluded from the report is the **union** of three sources, compared case-insensitively:

- `--ignore-ghsas=<csv>` on the command line
- `.npmrc ignore-ghsas=` (comma-separated)
- `package.json#gnpm.auditConfig.ignoreGhsas` (string list)

### 7.2 Audit on install

`gnpm install --audit-level=<sev>` runs an audit after the install completes and fails the install when any finding has severity at least `<sev>`. Default is unset (no post-install audit).

---

## 8. Workspaces

A workspace is declared in the root `package.json#workspaces` (string list or `{packages: [...]}`) or `pnpm-workspace.yaml#packages`. Globs follow the standard `**` / `*` semantics.

### 8.1 `workspace:` protocol

Forms in workspace package `package.json` dependencies:

| Form | Resolved to |
|---|---|
| `workspace:*` | the workspace's current version |
| `workspace:^` | `^<workspace version>` |
| `workspace:~` | `~<workspace version>` |
| `workspace:<range>` | the explicit range |

`workspace:` dependencies are linked rather than fetched from the registry. The destination is `node_modules/<name>` relative to the consuming workspace.

### 8.2 Workspace-to-workspace deps

When workspace `a` declares `workspace:` against workspace `b`, gnpm creates `a/node_modules/b -> b` (symlink). Transitive imports from `b` resolve via `b/node_modules/` per the standard Node module resolution.

---

## 9. Catalogs

A **catalog** is a `(name → range)` table defined in `pnpm-workspace.yaml`:

```yaml
catalogs:
  default:
    react: ^18.3.0
    react-dom: ^18.3.0
  testing:
    vitest: ^3.0.0
```

`catalogs.default` may also be written at the top level as `catalog` (singular).

### 9.1 Reference

A `package.json` references a catalog entry via the `catalog:` protocol:

| Form | Refers to |
|---|---|
| `catalog:` | the entry in `catalogs.default` |
| `catalog:<name>` | the entry in `catalogs.<name>` |

### 9.2 Modes (`catalogMode`)

| Value | Behavior |
|---|---|
| `manual` | `catalog:` references are resolved. Workspace `package.json` declarations that do not use `catalog:` are not affected. |
| `prefer` | When a package name has a catalog entry, the catalog range is used even if the workspace declared a different range. |
| `strict` | When a workspace declares a range that diverges from the catalog entry for the same name, the install fails. |

---

## 10. dlx

`gnpm dlx <pkg> [<args>...]` fetches a package into a per-spec cache and runs its binary. The project's `node_modules/` is not modified.

### 10.1 Cache

Cache root: `$HOME/.gnpm/dlx/<key>/`. `<key>` is derived from the set of installed packages (the `name → version` map fed to the temp `package.json`) as follows:

1. For each entry, render the UTF-8 string `<name>@<version>`. No quoting, no escaping; `<version>` is the resolution input (a range, tag, or exact version), not the resolved version.
2. Sort the rendered strings in ascending byte order (Go's `sort.Strings`). For ASCII names and versions this matches lexicographic byte order.
3. Join the sorted strings with a single `\n` (U+000A) separator. No trailing newline.
4. Compute the SHA-256 digest of the UTF-8 bytes of that joined string. Encode the digest as lowercase hexadecimal.
5. `<key>` is the first 16 characters of that hex digest.

Repeated invocations of the same spec reuse the cache; `gnpm clean` is responsible for pruning it.

### 10.2 Forms

| Invocation | Installed packages | Bin to run |
|---|---|---|
| `gnpm dlx <pkg>[@<spec>] [<args>...]` | `<pkg>` (default `latest`) | bin name = stripped scope of `<pkg>` |
| `gnpm dlx -p <pkg>... <bin> [<args>...]` | every `-p` spec | first positional |
| `gnpm dlx -p <pkg>... -c <bin> [<args>...]` | every `-p` spec | `<bin>` from `-c` |

`-c` / `--call` always wins; bin resolution otherwise consults the first installed package's `package.json#bin` (an entry matching the requested name, then — if exactly one bin is declared — that one bin's name).

### 10.3 Behavior

- Lifecycle scripts in the dlx target are **always** skipped. `dangerouslyAllowAllBuilds` does not apply.
- `optimisticRepeatInstall` is forced off so the cache directory is always re-linked.
- `--offline` forbids network; a cache miss exits non-zero.

---

## 11. Implementation notes (non-normative)

This section records the Go-specific design decisions behind the behavior above. Nothing here is a requirement; it exists so the contract and the implementation read consistently.

- **Module path / binary.** Module `github.com/koji-1009/gnpm`; the installed binary is `gnpm`. The private namespace (`~/.gnpm/`, `node_modules/.gnpm/`, `package.json#gnpm`, `GNPM_CONFIG_*`, `GNPM_HOST_NODE_MAJOR`) belongs to gnpm alone and is distinct from both npm's and pnpm's namespaces.

- **Concurrency.** Tarball fetch + ingest, file hashing, gzip/JSON decode, and the linker's filesystem batches run on goroutines bounded by a `golang.org/x/sync/semaphore` whose weight matches the HTTP concurrency limit, coordinated with `errgroup`. There is no separate worker-pool or background-isolate abstraction: Go's scheduler runs CPU-bound decode work on OS threads directly, so a packument parse never blocks unrelated fetches.

- **Content store.** A content-addressable store under `~/.gnpm/store` keyed by per-file SHA-512. Materializing a package into `node_modules` uses `clonefile(2)` on macOS (via `golang.org/x/sys/unix`), per-file hardlink on Linux/Windows, and a byte copy as the last-resort fallback (cross-device or unsupported filesystem). Ingest writes through a temp path and `os.Rename` so a concurrent ingest of the same tarball is safe.

- **Cryptography.** Tarball integrity is SHA-512 / SHA-1 SRI verified with `crypto/sha512` / `crypto/sha1`. Registry ECDSA P-256 signatures are verified with `crypto/ecdsa` + `crypto/x509` against the key served at `/-/npm/v1/keys`. No external crypto library or bundled native dylib is required, so the binary is a single statically linked executable.

- **Archives.** Tarballs are extracted with `archive/tar` over `compress/gzip`, streaming. Extraction sanitizes entry paths (rejecting `..` traversal and absolute paths) and refuses symlinks that escape the package root. Only the regular-file exec bit is honored from tar headers; setuid/setgid bits are dropped.

- **YAML.** `pnpm-lock.yaml` and `pnpm-workspace.yaml` are parsed with `gopkg.in/yaml.v3` at the node level so unknown keys survive a round trip. Everything else (`.npmrc`, `package.json`, `package-lock.json`) uses the standard library — `encoding/json` and a small line-based `.npmrc` parser.

- **CLI.** Dispatch is built on the standard library `flag` package plus a small command table; there is no third-party CLI framework, which keeps the dependency surface and binary minimal while still giving the precise exit-code control described in [§5.1](#51-exit-codes).

- **Resolver.** The version solver is a Pubgrub implementation with conflict-driven learning, plus npm extensions: `overrides` / nested overrides, best-effort transitive `optionalDependencies` (including platform-specific native siblings), `peerDependencies` (auto-install with `peerDependenciesMeta.optional` honored), preferred-version seeding from the existing lockfile, and `dist-tag` resolution. The semver dialect is npm's, implemented directly (Go's `golang.org/x/mod/semver` is not npm-compatible) and validated against the node-semver fixture corpus.

- **External dependencies.** The full third-party set is `gopkg.in/yaml.v3`, `golang.org/x/sync`, `golang.org/x/sys`, and `github.com/bmatcuk/doublestar/v4` (workspace `**` globbing). Everything else is the Go standard library.
