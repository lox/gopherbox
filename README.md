# gopherbox — Sandboxed Shell for AI Agents

A pure-Go sandboxed shell environment with virtual filesystem, inspired by [just-bash](https://github.com/vercel-labs/just-bash). Designed to give AI agents a safe bash-like execution environment where they cannot escape the boundaries you define.

## Motivation

AI agents need shell access to be productive — file manipulation, text processing, data wrangling. But giving agents a real shell is dangerous. They can modify files outside the workspace, exfiltrate data via network calls, or run destructive commands.

gopherbox provides a bash-compatible shell where:
- The filesystem is virtual (in-memory, overlay, or jailed to a directory)
- Network access is disabled by default (opt-in with URL allowlists)
- Execution is bounded (loop limits, recursion depth, timeouts)
- No real processes are spawned — every command is a pure Go function

## Project Status

### Implemented So Far

- Core public API is implemented: `New`, `Exec`, `ExecWith`, persistent filesystem state, and per-call env/cwd overrides.
- `mvdan.cc/sh/v3` integration is in place with custom execution and filesystem handlers for command dispatch and VFS-backed file access.
- Filesystem modes are implemented via `afero`: in-memory (`InMemoryFs`), copy-on-write overlay (`OverlayFs`), and jailed read/write (`ReadWriteFs`).
- Runtime safety controls are implemented for timeout, max command count, max output size, loop iteration limits, and function call depth limits.
- Network access is implemented as opt-in via `curl`, with URL prefix and HTTP method allowlisting.
- Command set from all planned phases is scaffolded and wired into the registry, with practical behaviour for common agent workflows.
- Automated tests cover API behaviour, persistence semantics, limits, VFS modes, custom commands, and network allowlist behaviour.

### Planned Next

- Tighten command compatibility and edge-case behaviour to better match POSIX/coreutils semantics.
- Expand command option coverage and improve error message fidelity for complex scripts.
- Continue hardening and profiling for larger scripts and higher-concurrency workloads.

## Key Dependencies

### Shell Interpreter: `mvdan.cc/sh/v3`

The [shfmt](https://github.com/mvdan/sh) project includes a full POSIX shell interpreter (`interp` package) with pluggable handlers:

- **`ExecHandler`** — intercept command execution → route to our Go command implementations
- **`OpenHandler`** — intercept file opens → route to our VFS
- **`ReadDirHandler`** — intercept directory reads → route to our VFS

This gives us a proper shell parser and interpreter for free: pipes, redirects, variables, globbing, control flow, functions, subshells.

### Virtual Filesystem: `github.com/spf13/afero`

Provides the filesystem abstraction layer:

- **`MemMapFs`** — pure in-memory, default for full sandboxing
- **`CopyOnWriteFs`** — overlay: reads from real disk, writes stay in memory
- **`BasePathFs`** — jail to a directory, prevents path traversal
- **`ReadOnlyFs`** — wrap any fs to make it read-only

These compose to create the same filesystem modes as just-bash:
- `MemMapFs` → InMemoryFs
- `CopyOnWriteFs(ReadOnlyFs(OsFs), MemMapFs)` → OverlayFs
- `BasePathFs(OsFs, root)` → ReadWriteFs (jailed)

## Architecture

```
gopherbox/
├── gopherbox.go       # Public API: Shell struct, Exec(), configuration
├── vfs.go             # VFS setup, filesystem mode helpers
├── commands.go        # Root command registry bridge
├── exec.go            # ExecHandler integration with mvdan/sh interp
├── limits.go          # Execution limits (loops, recursion, timeout)
├── network.go         # Optional curl with URL allowlist
├── commands/          # Built-in command package and implementations
│   ├── commands.go    # Command package types + built-in registry
│   ├── fileops.go     # cat, cp, mv, rm, mkdir, touch, ln, ls, stat, rmdir, tree
│   ├── textproc.go    # grep, sed, head, tail, sort, uniq, wc, cut, tr, rev
│   ├── data.go        # awk, find, xargs, jq, base64, md5sum/sha1sum/sha256sum
│   ├── archive.go     # tar, gzip/gunzip/zcat, curl
│   └── misc.go        # echo, printf, env, pwd, cd, du, date, seq, sleep, expr, etc.
├── cmd/gopherbox/     # CLI entrypoint (BusyBox-style multicall + script mode)
│   └── main.go
└── gopherbox_test.go  # Tests
```

## Public API

```go
package gopherbox

// Shell is a sandboxed shell execution environment.
type Shell struct {
    fs      afero.Fs
    env     map[string]string
    cwd     string
    limits  ExecutionLimits
    network *NetworkConfig
    cmds    map[string]CommandFunc
}

// Config configures a new Shell instance.
type Config struct {
    // Files to pre-populate in the virtual filesystem.
    Files map[string]string

    // Env sets initial environment variables.
    Env map[string]string

    // Cwd sets the starting working directory. Default: /home/user
    Cwd string

    // Filesystem mode. Default: in-memory.
    Fs afero.Fs

    // Limits configures execution bounds.
    Limits ExecutionLimits

    // Network enables curl with URL allowlist. Nil = no network.
    Network *NetworkConfig

    // CustomCommands registers additional commands.
    CustomCommands map[string]CommandFunc
}

// New creates a sandboxed shell environment.
func New(cfg Config) *Shell

// Result is the output of a shell command execution.
type Result struct {
    Stdout   string
    Stderr   string
    ExitCode int
}

// Exec runs a shell script and returns the result.
// Each call gets a fresh environment (env vars, cwd don't persist).
// The filesystem persists across calls.
func (s *Shell) Exec(ctx context.Context, script string) (*Result, error)

// ExecWith runs a shell script with per-call overrides.
func (s *Shell) ExecWith(ctx context.Context, script string, overrides ExecOptions) (*Result, error)

type ExecOptions struct {
    Env map[string]string
    Cwd string
}

// CommandFunc is the signature for a gopherbox command.
type CommandFunc func(ctx context.Context, args []string, io CommandIO) int

// CommandIO provides the command's I/O context.
type CommandIO struct {
    Stdin  io.Reader
    Stdout io.Writer
    Stderr io.Writer
    Fs     afero.Fs
    Env    map[string]string
    Cwd    string
}
```

### Filesystem Helpers

```go
// InMemoryFs returns a pure in-memory filesystem.
func InMemoryFs() afero.Fs

// OverlayFs returns a copy-on-write filesystem over a real directory.
// Reads come from disk, writes stay in memory.
func OverlayFs(root string) afero.Fs

// ReadWriteFs returns a jailed real filesystem.
// Reads and writes go to disk, but can't escape root.
func ReadWriteFs(root string) afero.Fs
```

### Execution Limits

```go
type ExecutionLimits struct {
    MaxTimeout       time.Duration // Per-exec timeout. Default: 30s
    MaxLoopIter      int           // Max iterations per loop. Default: 10000
    MaxCallDepth     int           // Max function recursion. Default: 50
    MaxCommandCount  int           // Max total commands per exec. Default: 10000
    MaxOutputBytes   int           // Max stdout+stderr size. Default: 1MB
}
```

### Network Config

```go
type NetworkConfig struct {
    // AllowedURLPrefixes restricts curl to URLs starting with these prefixes.
    AllowedURLPrefixes []string

    // AllowedMethods restricts HTTP methods. Default: GET, HEAD.
    AllowedMethods []string
}
```

## Compatibility Targets

- **Primary target**: POSIX shell semantics (via `mvdan/sh`) plus a BusyBox-style tool contract for built-in commands.
- **Secondary reference**: `/bin/sh` behaviour for script semantics and exit-status behaviour in CI parity tests.
- **Non-goals by default**: exact BSD (macOS) flag parity and full GNU coreutils flag parity unless a concrete agent workflow requires it.

## Tooling Worklist

- [x] **Phase 1 — File Operations parity sweep**
  - Commands: `cat`, `cp`, `ls`, `mkdir`, `mv`, `rm`, `rmdir`, `touch`, `ln`, `stat`, `readlink`, `tree`, `file`
  - Focus: path semantics, error messages, overwrite/recursive behaviour, symlink edges.

- [ ] **Phase 2 — Text Processing parity sweep**
  - Commands: `grep` (+ `egrep`, `fgrep`), `sed`, `head`, `tail`, `sort`, `uniq`, `wc`, `cut`, `tr`, `rev`, `tac`, `paste`, `fold`, `nl`, `expand`, `unexpand`, `column`, `comm`, `join`, `diff`, `strings`
  - Focus: option handling, stdin/file precedence, exit-status fidelity for no-match/partial-match cases.

- [ ] **Phase 3 — Data & Search parity sweep**
  - Commands: `awk`, `find`, `xargs`, `jq`, `base64`, `md5sum`, `sha1sum`, `sha256sum`
  - Focus: expression compatibility, traversal behaviour, tokenisation/quoting with `xargs`, deterministic hash output format.

- [ ] **Phase 4 — Archives & Network parity sweep**
  - Commands: `tar`, `gzip`/`gunzip`/`zcat`, `curl` (with allowlist)
  - Focus: archive round-trips, compression flags, HTTP method restrictions, URL allowlist diagnostics.

- [ ] **Phase 5 — Shell Utility parity sweep**
  - Commands: `echo`, `printf`, `env`, `export`, `printenv`, `pwd`, `cd`, `basename`, `dirname`, `du`, `date`, `seq`, `sleep`, `expr`, `true`, `false`, `which`, `whoami`, `hostname`, `tee`, `chmod`, `time`, `timeout`
  - Focus: shell integration (`$?`, environment mutation, cwd transitions), timing/timeout semantics, option compatibility.

- [ ] **Cross-phase completion criteria**
  - [x] Add process-level CLI parity tests (`gopherbox` vs `/bin/sh`) for representative workflows in each phase.
  - [ ] Document intentional deviations from BusyBox/POSIX in README as explicit compatibility notes.
  - [x] Keep `go test ./...` and `go test -race ./...` green with new parity coverage.

## Standalone Usage

gopherbox is a standalone package and does not require any specific agent framework.

```go
package main

import (
    "context"
    "fmt"

    "github.com/buildkite/gopherbox"
)

func main() {
    shell := gopherbox.New(gopherbox.Config{
        Files: map[string]string{
            "/home/user/input.txt": "hello\nworld\n",
        },
    })

    result, err := shell.Exec(context.Background(), `cat input.txt | grep hello`)
    if err != nil {
        panic(err)
    }

    fmt.Printf("exit=%d\n", result.ExitCode)
    fmt.Printf("stdout=%q\n", result.Stdout)
    fmt.Printf("stderr=%q\n", result.Stderr)
}
```

## CLI Usage

You can run gopherbox as a standalone CLI:

```bash
# Run a script
go run ./cmd/gopherbox -c 'echo hello; pwd'

# BusyBox-style shell applet mode
go run ./cmd/gopherbox sh -c 'echo hello from sh mode'

# BusyBox-style command invocation
go run ./cmd/gopherbox --root . cat README.md

# Writes are overlay by default (in-memory); use --rw to write through
go run ./cmd/gopherbox --root . --rw touch created-on-disk.txt
```

The CLI supports BusyBox-style multicall behaviour. If the binary is invoked via a symlink named after a built-in command, that command is executed directly. `sh` command mode is also supported for non-interactive `-c` and script-file execution.

## What's Out of Scope (v1)

- **WASM execution** — no binary execution, everything is pure Go
- **Python/SQLite** — just-bash supports these via Pyodide/sql.js; we skip them for now
- **Process isolation** — no OS-level sandboxing; the safety model is "commands are Go functions operating on a VFS"
- **Interactive mode** — exec-only; no REPL
- **Full POSIX compliance** — aim for "good enough for agents", not certification

## References

- [just-bash](https://github.com/vercel-labs/just-bash) — TypeScript sandboxed shell for agents (direct inspiration)
- [mvdan/sh](https://github.com/mvdan/sh) — Go shell parser and interpreter
- [afero](https://github.com/spf13/afero) — Go filesystem abstraction
- [u-root](https://github.com/u-root/u-root) — Pure Go reimplementations of coreutils (reference for command implementations)
