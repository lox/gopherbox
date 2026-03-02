package gopherbox

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"path/filepath"
	"sync"
	"sync/atomic"

	"github.com/spf13/afero"
	"mvdan.cc/sh/v3/expand"
	"mvdan.cc/sh/v3/interp"
	"mvdan.cc/sh/v3/syntax"
)

type execState struct {
	mu  sync.RWMutex
	cwd string
}

func (s *execState) cwdValue() string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.cwd == "" {
		return "/"
	}
	return s.cwd
}

func (s *execState) setCwd(cwd string) {
	s.mu.Lock()
	s.cwd = cleanAbsolute(cwd)
	s.mu.Unlock()
}

// ExecWith runs a shell script with per-call overrides.
func (s *Shell) ExecWith(ctx context.Context, script string, overrides ExecOptions) (*Result, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	limits := s.limits.withDefaults()
	env := cloneEnv(s.env)
	for k, v := range overrides.Env {
		env[k] = v
	}

	cwd := s.cwd
	if overrides.Cwd != "" {
		cwd = resolvePath(s.cwd, overrides.Cwd)
	}
	info, err := s.fs.Stat(cwd)
	if err != nil || !info.IsDir() {
		return nil, fmt.Errorf("gopherbox: cwd does not exist: %s", cwd)
	}
	env["PWD"] = cwd

	state := &execState{cwd: cwd}

	cmds := make(map[string]CommandFunc, len(s.cmds)+2)
	for name, fn := range s.cmds {
		cmds[name] = fn
	}
	cmds["__gopherbox_cd"] = func(_ context.Context, args []string, ioCtx CommandIO) int {
		target := ioCtx.Env["HOME"]
		if target == "" {
			target = "/"
		}
		if len(args) > 0 {
			target = args[0]
		}
		abs := resolvePath(state.cwdValue(), target)
		entry, err := ioCtx.Fs.Stat(abs)
		if err != nil || !entry.IsDir() {
			writeErrf(ioCtx, "cd: %s: no such directory\n", target)
			return 1
		}
		state.setCwd(abs)
		return 0
	}
	cmds["__gopherbox_pwd"] = func(_ context.Context, _ []string, ioCtx CommandIO) int {
		_, _ = fmt.Fprintln(ioCtx.Stdout, state.cwdValue())
		return 0
	}

	stdoutBuf := &lockedBuffer{}
	stderrBuf := &lockedBuffer{}

	counter := &atomic.Int64{}
	overflow := &atomic.Bool{}
	stdout := &limitedWriter{target: stdoutBuf, total: counter, limit: int64(limits.MaxOutputBytes), overflow: overflow}
	stderr := &limitedWriter{target: stderrBuf, total: counter, limit: int64(limits.MaxOutputBytes), overflow: overflow}

	pairs := make([]string, 0, len(env))
	for _, k := range sortedKeys(env) {
		pairs = append(pairs, fmt.Sprintf("%s=%s", k, env[k]))
	}

	var cmdCount atomic.Int64
	runner, err := interp.New(
		interp.Env(expand.ListEnviron(pairs...)),
		interp.Dir("/"),
		interp.StdIO(bytes.NewReader(nil), stdout, stderr),
		interp.CallHandler(callHandler()),
		interp.ExecHandler(s.execHandler(&cmdCount, limits, cmds, state)),
		interp.OpenHandler(s.openHandler(state)),
		interp.ReadDirHandler2(s.readDirHandler(state)),
		interp.StatHandler(s.statHandler(state)),
	)
	if err != nil {
		return nil, err
	}

	parser := syntax.NewParser(syntax.Variant(syntax.LangBash))
	file, err := parser.Parse(bytes.NewBufferString(script), "gopherbox")
	if err != nil {
		return nil, err
	}

	runCtx := ctx
	cancel := func() {}
	if limits.MaxTimeout > 0 {
		runCtx, cancel = context.WithTimeout(ctx, limits.MaxTimeout)
	}
	defer cancel()

	runErr := runner.Run(runCtx, file)
	if errors.Is(runCtx.Err(), context.DeadlineExceeded) {
		return nil, ErrTimeoutExceeded
	}
	if overflow.Load() || errors.Is(runErr, ErrOutputLimitExceeded) {
		return nil, ErrOutputLimitExceeded
	}
	if errors.Is(runErr, ErrCommandLimitExceeded) {
		return nil, ErrCommandLimitExceeded
	}

	result := &Result{
		Stdout: stdoutBuf.String(),
		Stderr: stderrBuf.String(),
	}

	if runErr != nil {
		var exit interp.ExitStatus
		if errors.As(runErr, &exit) {
			result.ExitCode = int(exit)
			return result, nil
		}
		if errors.Is(runErr, context.Canceled) {
			return nil, context.Canceled
		}
		if errors.Is(runErr, context.DeadlineExceeded) {
			return nil, ErrTimeoutExceeded
		}
		return nil, runErr
	}

	return result, nil
}

func (s *Shell) execHandler(cmdCount *atomic.Int64, limits ExecutionLimits, cmds map[string]CommandFunc, state *execState) interp.ExecHandlerFunc {
	return func(ctx context.Context, args []string) error {
		if len(args) == 0 {
			return nil
		}
		if cmdCount.Add(1) > int64(limits.MaxCommandCount) {
			return ErrCommandLimitExceeded
		}

		hc, ok := safeHandlerCtx(ctx)
		if !ok {
			return fmt.Errorf("gopherbox: missing handler context")
		}

		cmdName := args[0]
		cmd, ok := cmds[cmdName]
		if !ok {
			_, _ = fmt.Fprintf(hc.Stderr, "%s: command not found\n", cmdName)
			return interp.ExitStatus(127)
		}

		envMap := environToMap(hc.Env)
		envMap["PWD"] = state.cwdValue()

		commandIO := CommandIO{
			Stdin:   hc.Stdin,
			Stdout:  hc.Stdout,
			Stderr:  hc.Stderr,
			Fs:      s.fs,
			Env:     envMap,
			Cwd:     state.cwdValue(),
			Network: s.network,
			Cmds:    cmds,
		}

		exitCode := cmd(ctx, args[1:], commandIO)
		if exitCode == 0 {
			return nil
		}
		if exitCode < 0 {
			exitCode = 1
		}
		if exitCode > 255 {
			exitCode = 255
		}
		return interp.ExitStatus(uint8(exitCode))
	}
}

func (s *Shell) openHandler(state *execState) interp.OpenHandlerFunc {
	return func(_ context.Context, path string, flag int, perm fs.FileMode) (io.ReadWriteCloser, error) {
		abs := path
		if path == "/dev/null" {
			return devNullFile{}, nil
		}
		if !filepath.IsAbs(path) {
			abs = resolvePath(state.cwdValue(), path)
		}
		f, err := s.fs.OpenFile(abs, flag, perm)
		if err != nil {
			return nil, err
		}
		return f, nil
	}
}

func (s *Shell) readDirHandler(state *execState) interp.ReadDirHandlerFunc2 {
	return func(_ context.Context, path string) ([]fs.DirEntry, error) {
		abs := path
		if !filepath.IsAbs(path) {
			abs = resolvePath(state.cwdValue(), path)
		}
		infos, err := afero.ReadDir(s.fs, abs)
		if err != nil {
			return nil, err
		}
		entries := make([]fs.DirEntry, 0, len(infos))
		for _, info := range infos {
			entries = append(entries, fs.FileInfoToDirEntry(info))
		}
		return entries, nil
	}
}

func (s *Shell) statHandler(state *execState) interp.StatHandlerFunc {
	return func(_ context.Context, name string, followSymlinks bool) (fs.FileInfo, error) {
		abs := name
		if !filepath.IsAbs(name) {
			abs = resolvePath(state.cwdValue(), name)
		}
		if followSymlinks {
			return s.fs.Stat(abs)
		}
		if lstater, ok := s.fs.(afero.Lstater); ok {
			info, _, err := lstater.LstatIfPossible(abs)
			return info, err
		}
		return s.fs.Stat(abs)
	}
}

func callHandler() interp.CallHandlerFunc {
	return func(_ context.Context, args []string) ([]string, error) {
		if len(args) == 0 {
			return args, nil
		}
		switch args[0] {
		case "cd", "pwd":
			out := append([]string(nil), args...)
			out[0] = "__gopherbox_" + args[0]
			return out, nil
		default:
			return args, nil
		}
	}
}

func safeHandlerCtx(ctx context.Context) (_ interp.HandlerContext, ok bool) {
	defer func() {
		if recover() != nil {
			ok = false
		}
	}()
	hc := interp.HandlerCtx(ctx)
	return hc, true
}

func environToMap(env expand.Environ) map[string]string {
	out := map[string]string{}
	env.Each(func(name string, vr expand.Variable) bool {
		if vr.IsSet() {
			out[name] = vr.String()
		}
		return true
	})
	return out
}

type lockedBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (b *lockedBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.b.Write(p)
}

func (b *lockedBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.b.String()
}

type limitedWriter struct {
	target   io.Writer
	total    *atomic.Int64
	limit    int64
	overflow *atomic.Bool
}

func (w *limitedWriter) Write(p []byte) (int, error) {
	if w.limit <= 0 {
		return w.target.Write(p)
	}

	current := w.total.Load()
	if current >= w.limit {
		w.overflow.Store(true)
		return len(p), nil
	}

	available := int(w.limit - current)
	if available <= 0 {
		w.overflow.Store(true)
		return len(p), nil
	}

	chunk := p
	if len(chunk) > available {
		chunk = chunk[:available]
		w.overflow.Store(true)
	}

	n, err := w.target.Write(chunk)
	w.total.Add(int64(n))
	if err != nil {
		return n, err
	}
	if len(chunk) < len(p) {
		return len(p), nil
	}
	return n, nil
}

type devNullFile struct{}

func (devNullFile) Read(_ []byte) (int, error)  { return 0, io.EOF }
func (devNullFile) Write(p []byte) (int, error) { return len(p), nil }
func (devNullFile) Close() error                { return nil }
