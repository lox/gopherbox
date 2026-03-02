package gopherbox

import (
	"context"
	"path/filepath"
	"sort"
	"strings"
	"sync"

	gcmd "github.com/buildkite/gopherbox/commands"
	"github.com/spf13/afero"
)

// Shell is a sandboxed shell execution environment.
type Shell struct {
	mu      sync.Mutex
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

	// Cwd sets the starting working directory. Default: /home/user.
	Cwd string

	// Fs sets the filesystem mode. Default: in-memory.
	Fs afero.Fs

	// Limits configures execution bounds.
	Limits ExecutionLimits

	// Network enables curl with URL allowlist. Nil = no network.
	Network *NetworkConfig

	// CustomCommands registers additional commands.
	CustomCommands map[string]CommandFunc
}

// Result is the output of a shell command execution.
type Result struct {
	Stdout   string
	Stderr   string
	ExitCode int
}

// ExecOptions configures per-call execution overrides.
type ExecOptions struct {
	Env map[string]string
	Cwd string
}

// CommandFunc is the signature for a gopherbox command.
type CommandFunc = gcmd.CommandFunc

// CommandIO provides the command's I/O context.
type CommandIO = gcmd.CommandIO

// New creates a sandboxed shell environment.
func New(cfg Config) *Shell {
	fs := cfg.Fs
	if fs == nil {
		fs = InMemoryFs()
	}

	cwd := cfg.Cwd
	if cwd == "" {
		cwd = "/home/user"
	}
	cwd = cleanAbsolute(cwd)

	env := defaultEnv(cwd)
	for k, v := range cfg.Env {
		env[k] = v
	}

	s := &Shell{
		fs:      fs,
		env:     env,
		cwd:     cwd,
		limits:  cfg.Limits.withDefaults(),
		network: cfg.Network,
		cmds:    defaultCommands(),
	}

	for name, fn := range cfg.CustomCommands {
		s.cmds[name] = fn
	}

	// Create baseline directory structure for a predictable shell environment.
	_ = fs.MkdirAll(cwd, 0o755)

	for path, content := range cfg.Files {
		abs := cleanAbsolute(resolvePath(cwd, path))
		_ = fs.MkdirAll(filepath.Dir(abs), 0o755)
		_ = afero.WriteFile(fs, abs, []byte(content), 0o644)
	}

	return s
}

// Exec runs a shell script and returns the result.
// Each call gets a fresh environment (env vars and cwd do not persist).
// The filesystem persists across calls.
func (s *Shell) Exec(ctx context.Context, script string) (*Result, error) {
	return s.ExecWith(ctx, script, ExecOptions{})
}

func defaultEnv(cwd string) map[string]string {
	env := map[string]string{
		"HOME":     "/home/user",
		"USER":     "user",
		"LOGNAME":  "user",
		"PWD":      cwd,
		"PATH":     "/usr/local/bin:/usr/bin:/bin",
		"LANG":     "C.UTF-8",
		"HOSTNAME": "gopherbox",
	}
	return env
}

func cloneEnv(in map[string]string) map[string]string {
	out := make(map[string]string, len(in))
	for k, v := range in {
		out[k] = v
	}
	return out
}

func cleanAbsolute(p string) string {
	if p == "" {
		return "/"
	}
	if !strings.HasPrefix(p, "/") {
		p = "/" + p
	}
	return filepath.Clean(p)
}

func resolvePath(cwd, p string) string {
	if p == "" {
		return cleanAbsolute(cwd)
	}
	if filepath.IsAbs(p) {
		return cleanAbsolute(p)
	}
	return cleanAbsolute(filepath.Join(cwd, p))
}

func sortedKeys[K ~string, V any](m map[K]V) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, string(k))
	}
	sort.Strings(keys)
	return keys
}
