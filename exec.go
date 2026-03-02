package gopherbox

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"

	gcmd "github.com/buildkite/gopherbox/commands"
	"github.com/spf13/afero"
	"mvdan.cc/sh/v3/expand"
	"mvdan.cc/sh/v3/interp"
	"mvdan.cc/sh/v3/syntax"
)

const (
	internalLoopIterCmd  = "__gopherbox_loop_iter"
	internalFuncEnterCmd = "__gopherbox_func_enter"
	internalFuncLeaveCmd = "__gopherbox_func_leave"
)

type execLimitState struct {
	mu           sync.Mutex
	maxLoopIter  int
	maxCallDepth int
	loopCounts   map[int]int
	callDepth    int
}

func newExecLimitState(limits ExecutionLimits) *execLimitState {
	return &execLimitState{
		maxLoopIter:  limits.MaxLoopIter,
		maxCallDepth: limits.MaxCallDepth,
		loopCounts:   map[int]int{},
	}
}

func (s *execLimitState) recordLoopIter(loopID int) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.loopCounts[loopID]++
	if s.loopCounts[loopID] > s.maxLoopIter {
		return ErrLoopLimitExceeded
	}
	return nil
}

func (s *execLimitState) enterFunction() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.callDepth++
	if s.callDepth > s.maxCallDepth {
		return ErrCallDepthExceeded
	}
	return nil
}

func (s *execLimitState) leaveFunction() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.callDepth > 0 {
		s.callDepth--
	}
}

type scriptInstrumenter struct {
	parser     *syntax.Parser
	nextLoopID int
}

func newScriptInstrumenter() *scriptInstrumenter {
	return &scriptInstrumenter{parser: syntax.NewParser(syntax.Variant(syntax.LangBash))}
}

func (i *scriptInstrumenter) instrument(file *syntax.File) error {
	return i.instrumentStmts(file.Stmts)
}

func (i *scriptInstrumenter) instrumentStmts(stmts []*syntax.Stmt) error {
	for _, stmt := range stmts {
		if err := i.instrumentStmt(stmt); err != nil {
			return err
		}
	}
	return nil
}

func (i *scriptInstrumenter) instrumentStmt(stmt *syntax.Stmt) error {
	if stmt == nil || stmt.Cmd == nil {
		return nil
	}
	return i.instrumentCmd(stmt.Cmd)
}

func (i *scriptInstrumenter) instrumentCmd(cmd syntax.Command) error {
	switch c := cmd.(type) {
	case *syntax.Block:
		return i.instrumentStmts(c.Stmts)
	case *syntax.Subshell:
		return i.instrumentStmts(c.Stmts)
	case *syntax.IfClause:
		return i.instrumentIfClause(c)
	case *syntax.WhileClause:
		if err := i.instrumentStmts(c.Cond); err != nil {
			return err
		}
		if err := i.instrumentStmts(c.Do); err != nil {
			return err
		}
		i.nextLoopID++
		loopStmt, err := i.internalStmt(fmt.Sprintf("%s %d", internalLoopIterCmd, i.nextLoopID))
		if err != nil {
			return err
		}
		c.Do = append([]*syntax.Stmt{loopStmt}, c.Do...)
		return nil
	case *syntax.ForClause:
		if err := i.instrumentStmts(c.Do); err != nil {
			return err
		}
		i.nextLoopID++
		loopStmt, err := i.internalStmt(fmt.Sprintf("%s %d", internalLoopIterCmd, i.nextLoopID))
		if err != nil {
			return err
		}
		c.Do = append([]*syntax.Stmt{loopStmt}, c.Do...)
		return nil
	case *syntax.CaseClause:
		for _, item := range c.Items {
			if err := i.instrumentStmts(item.Stmts); err != nil {
				return err
			}
		}
		return nil
	case *syntax.BinaryCmd:
		if err := i.instrumentStmt(c.X); err != nil {
			return err
		}
		return i.instrumentStmt(c.Y)
	case *syntax.TimeClause:
		return i.instrumentStmt(c.Stmt)
	case *syntax.CoprocClause:
		return i.instrumentStmt(c.Stmt)
	case *syntax.FuncDecl:
		body := c.Body
		if body == nil {
			body = &syntax.Stmt{Cmd: &syntax.Block{}}
		}
		if err := i.instrumentStmt(body); err != nil {
			return err
		}
		enterStmt, err := i.internalStmt(internalFuncEnterCmd)
		if err != nil {
			return err
		}
		leaveStmt, err := i.internalStmt(internalFuncLeaveCmd + " $?")
		if err != nil {
			return err
		}
		c.Body = &syntax.Stmt{
			Position: body.Position,
			Cmd: &syntax.Block{
				Stmts: []*syntax.Stmt{enterStmt, body, leaveStmt},
			},
		}
		return nil
	default:
		return nil
	}
}

func (i *scriptInstrumenter) instrumentIfClause(clause *syntax.IfClause) error {
	if clause == nil {
		return nil
	}
	if err := i.instrumentStmts(clause.Cond); err != nil {
		return err
	}
	if err := i.instrumentStmts(clause.Then); err != nil {
		return err
	}
	return i.instrumentIfClause(clause.Else)
}

func (i *scriptInstrumenter) internalStmt(command string) (*syntax.Stmt, error) {
	file, err := i.parser.Parse(bytes.NewBufferString(command+"\n"), "gopherbox-internal")
	if err != nil {
		return nil, err
	}
	if len(file.Stmts) != 1 || file.Stmts[0] == nil {
		return nil, fmt.Errorf("gopherbox: internal command parse failed: %q", command)
	}
	return file.Stmts[0], nil
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

	cmds := make(map[string]CommandFunc, len(s.cmds)+1)
	for name, fn := range s.cmds {
		cmds[name] = fn
	}
	cmds["__gopherbox_pwd"] = func(_ context.Context, _ []string, ioCtx CommandIO) int {
		_, _ = fmt.Fprintln(ioCtx.Stdout, ioCtx.Cwd)
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

	parser := syntax.NewParser(syntax.Variant(syntax.LangBash))
	file, err := parser.Parse(bytes.NewBufferString(script), "gopherbox")
	if err != nil {
		return nil, err
	}
	instrumenter := newScriptInstrumenter()
	if err := instrumenter.instrument(file); err != nil {
		return nil, err
	}

	limitState := newExecLimitState(limits)

	interpRoot, err := os.MkdirTemp("", "gopherbox-interp-*")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(interpRoot)

	interpDir := hostPathForVirtual(interpRoot, cwd)
	if err := os.MkdirAll(interpDir, 0o755); err != nil {
		return nil, err
	}

	var cmdCount atomic.Int64
	runner, err := interp.New(
		interp.Env(expand.ListEnviron(pairs...)),
		interp.Dir(interpDir),
		interp.StdIO(bytes.NewReader(nil), stdout, stderr),
		interp.CallHandler(callHandler(s.fs, interpRoot, limitState)),
		interp.ExecHandler(s.execHandler(&cmdCount, limits, cmds, interpRoot, limitState)),
		interp.OpenHandler(s.openHandler(interpRoot)),
		interp.ReadDirHandler2(s.readDirHandler(interpRoot)),
		interp.StatHandler(s.statHandler(interpRoot)),
	)
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
	if errors.Is(runErr, ErrLoopLimitExceeded) {
		return nil, ErrLoopLimitExceeded
	}
	if errors.Is(runErr, ErrCallDepthExceeded) {
		return nil, ErrCallDepthExceeded
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

func (s *Shell) execHandler(cmdCount *atomic.Int64, limits ExecutionLimits, cmds map[string]CommandFunc, interpRoot string, limitState *execLimitState) interp.ExecHandlerFunc {
	return func(ctx context.Context, args []string) error {
		if len(args) == 0 {
			return nil
		}

		switch args[0] {
		case internalLoopIterCmd:
			if len(args) != 2 {
				return fmt.Errorf("gopherbox: invalid %s invocation", internalLoopIterCmd)
			}
			loopID, err := strconv.Atoi(args[1])
			if err != nil {
				return fmt.Errorf("gopherbox: invalid %s argument: %q", internalLoopIterCmd, args[1])
			}
			return limitState.recordLoopIter(loopID)
		case internalFuncEnterCmd:
			return limitState.enterFunction()
		case internalFuncLeaveCmd:
			if len(args) > 2 {
				return fmt.Errorf("gopherbox: invalid %s invocation", internalFuncLeaveCmd)
			}
			limitState.leaveFunction()
			if len(args) == 1 {
				return nil
			}
			status, err := strconv.Atoi(args[1])
			if err != nil {
				return fmt.Errorf("gopherbox: invalid %s argument: %q", internalFuncLeaveCmd, args[1])
			}
			if status <= 0 {
				if status < 0 {
					return interp.ExitStatus(1)
				}
				return nil
			}
			if status > 255 {
				status = 255
			}
			return interp.ExitStatus(uint8(status))
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

		cwd := virtualCwd(ctx, interpRoot)
		envMap := environToMap(hc.Env)
		envMap["PWD"] = cwd

		var network gcmd.NetworkPolicy
		if s.network != nil {
			network = s.network
		}

		commandIO := CommandIO{
			Stdin:   hc.Stdin,
			Stdout:  hc.Stdout,
			Stderr:  hc.Stderr,
			Fs:      s.fs,
			Env:     envMap,
			Cwd:     cwd,
			Network: network,
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

func (s *Shell) openHandler(interpRoot string) interp.OpenHandlerFunc {
	return func(ctx context.Context, path string, flag int, perm fs.FileMode) (io.ReadWriteCloser, error) {
		if path == "/dev/null" {
			return devNullFile{}, nil
		}
		abs := resolveExecutionPath(ctx, path, interpRoot)
		f, err := s.fs.OpenFile(abs, flag, perm)
		if err != nil {
			return nil, err
		}
		return f, nil
	}
}

func (s *Shell) readDirHandler(interpRoot string) interp.ReadDirHandlerFunc2 {
	return func(ctx context.Context, path string) ([]fs.DirEntry, error) {
		abs := resolveExecutionPath(ctx, path, interpRoot)
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

func (s *Shell) statHandler(interpRoot string) interp.StatHandlerFunc {
	return func(ctx context.Context, name string, followSymlinks bool) (fs.FileInfo, error) {
		abs := resolveExecutionPath(ctx, name, interpRoot)
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

func mapInterpPath(path, interpRoot string) (string, bool) {
	if !filepath.IsAbs(path) {
		return "", false
	}
	base := filepath.Clean(interpRoot)
	if base == "/" {
		return "", false
	}
	rel, err := filepath.Rel(base, filepath.Clean(path))
	if err != nil {
		return "", false
	}
	if rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", false
	}
	if rel == "." {
		return "/", true
	}
	return cleanAbsolute(filepath.Join("/", rel)), true
}

func hostPathForVirtual(interpRoot, virtualPath string) string {
	virtualPath = cleanAbsolute(virtualPath)
	if virtualPath == "/" {
		return interpRoot
	}
	return filepath.Join(interpRoot, strings.TrimPrefix(virtualPath, "/"))
}

func callHandler(vfs afero.Fs, interpRoot string, limitState *execLimitState) interp.CallHandlerFunc {
	return func(ctx context.Context, args []string) ([]string, error) {
		if len(args) == 0 {
			return args, nil
		}
		switch args[0] {
		case "cd":
			cwd := virtualCwd(ctx, interpRoot)
			env := map[string]string{}
			if hc, ok := safeHandlerCtx(ctx); ok {
				env = environToMap(hc.Env)
			}

			target := env["HOME"]
			if target == "" {
				target = "/"
			}
			if len(args) > 1 {
				target = args[1]
			}
			if target == "-" {
				return args, nil
			}

			virtualTarget := resolvePath(cwd, target)
			hostTarget := filepath.Join(interpRoot, ".missing")
			if info, err := vfs.Stat(virtualTarget); err == nil && info.IsDir() {
				hostTarget = hostPathForVirtual(interpRoot, virtualTarget)
				_ = os.MkdirAll(hostTarget, 0o755)
			} else {
				if hc, ok := safeHandlerCtx(ctx); ok {
					_, _ = fmt.Fprintf(hc.Stderr, "cd: %s: no such file or directory\n", target)
				}
			}

			out := append([]string(nil), args...)
			if len(out) > 1 {
				out[1] = hostTarget
			} else {
				out = append(out, hostTarget)
			}
			return out, nil
		case "pwd":
			out := append([]string(nil), args...)
			out[0] = "__gopherbox_" + args[0]
			return out, nil
		case "return":
			limitState.leaveFunction()
			return args, nil
		default:
			return args, nil
		}
	}
}

func virtualCwd(ctx context.Context, interpRoot string) string {
	hc, ok := safeHandlerCtx(ctx)
	if !ok {
		return "/"
	}
	if mapped, ok := mapInterpPath(hc.Dir, interpRoot); ok {
		return mapped
	}
	return cleanAbsolute(hc.Dir)
}

func resolveExecutionPath(ctx context.Context, path, interpRoot string) string {
	if mapped, ok := mapInterpPath(path, interpRoot); ok {
		return mapped
	}
	if filepath.IsAbs(path) {
		return cleanAbsolute(path)
	}
	return resolvePath(virtualCwd(ctx, interpRoot), path)
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
