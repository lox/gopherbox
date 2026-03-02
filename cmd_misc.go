package gopherbox

import (
	"context"
	"errors"
	"fmt"
	"io"
	"math"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/spf13/afero"
)

func cmdEcho(_ context.Context, args []string, ioCtx CommandIO) int {
	_, _ = fmt.Fprintln(ioCtx.Stdout, strings.Join(args, " "))
	return 0
}

func cmdPrintf(_ context.Context, args []string, ioCtx CommandIO) int {
	if len(args) == 0 {
		writeErrf(ioCtx, "printf: missing format\n")
		return 1
	}
	format := args[0]
	vals := make([]any, 0, len(args)-1)
	for _, arg := range args[1:] {
		vals = append(vals, arg)
	}
	_, err := fmt.Fprintf(ioCtx.Stdout, format, vals...)
	if err != nil {
		writeErrf(ioCtx, "printf: %v\n", err)
		return 1
	}
	return 0
}

func cmdEnv(ctx context.Context, args []string, ioCtx CommandIO) int {
	env := cloneEnv(ioCtx.Env)
	i := 0
	for i < len(args) && strings.Contains(args[i], "=") && !strings.HasPrefix(args[i], "-") {
		parts := strings.SplitN(args[i], "=", 2)
		env[parts[0]] = parts[1]
		i++
	}
	if i < len(args) {
		next := ioCtx
		next.Env = env
		return runSubcommand(ctx, args[i:], next)
	}
	for _, key := range sortedKeys(env) {
		_, _ = fmt.Fprintf(ioCtx.Stdout, "%s=%s\n", key, env[key])
	}
	return 0
}

func cmdPrintenv(_ context.Context, args []string, ioCtx CommandIO) int {
	if len(args) == 0 {
		for _, key := range sortedKeys(ioCtx.Env) {
			_, _ = fmt.Fprintf(ioCtx.Stdout, "%s=%s\n", key, ioCtx.Env[key])
		}
		return 0
	}
	exitCode := 0
	for _, key := range args {
		val, ok := ioCtx.Env[key]
		if !ok {
			exitCode = 1
			continue
		}
		_, _ = fmt.Fprintln(ioCtx.Stdout, val)
	}
	return exitCode
}

func cmdPwd(_ context.Context, _ []string, ioCtx CommandIO) int {
	_, _ = fmt.Fprintln(ioCtx.Stdout, ioCtx.Cwd)
	return 0
}

func cmdCd(_ context.Context, args []string, ioCtx CommandIO) int {
	target := ioCtx.Env["HOME"]
	if target == "" {
		target = "/"
	}
	if len(args) > 0 {
		target = args[0]
	}
	abs := resolvePath(ioCtx.Cwd, target)
	info, err := ioCtx.Fs.Stat(abs)
	if err != nil {
		writeErrf(ioCtx, "cd: %v\n", err)
		return 1
	}
	if !info.IsDir() {
		writeErrf(ioCtx, "cd: not a directory: %s\n", target)
		return 1
	}
	return 0
}

func cmdBasename(_ context.Context, args []string, ioCtx CommandIO) int {
	if len(args) == 0 {
		writeErrf(ioCtx, "basename: missing operand\n")
		return 1
	}
	_, _ = fmt.Fprintln(ioCtx.Stdout, filepath.Base(args[0]))
	return 0
}

func cmdDirname(_ context.Context, args []string, ioCtx CommandIO) int {
	if len(args) == 0 {
		writeErrf(ioCtx, "dirname: missing operand\n")
		return 1
	}
	_, _ = fmt.Fprintln(ioCtx.Stdout, filepath.Dir(args[0]))
	return 0
}

func cmdDu(_ context.Context, args []string, ioCtx CommandIO) int {
	target := "."
	if len(args) > 0 {
		target = args[0]
	}
	abs := resolvePath(ioCtx.Cwd, target)

	total, err := dirSize(ioCtx, abs)
	if err != nil {
		writeErrf(ioCtx, "du: %v\n", err)
		return 1
	}
	_, _ = fmt.Fprintf(ioCtx.Stdout, "%d\t%s\n", total, target)
	return 0
}

func dirSize(ioCtx CommandIO, path string) (int64, error) {
	info, err := ioCtx.Fs.Stat(path)
	if err != nil {
		return 0, err
	}
	if !info.IsDir() {
		return info.Size(), nil
	}
	var total int64
	entries, err := afero.ReadDir(ioCtx.Fs, path)
	if err != nil {
		return 0, err
	}
	for _, entry := range entries {
		sz, err := dirSize(ioCtx, filepath.Join(path, entry.Name()))
		if err != nil {
			return 0, err
		}
		total += sz
	}
	return total, nil
}

func cmdDate(_ context.Context, args []string, ioCtx CommandIO) int {
	now := time.Now()
	if len(args) == 1 && strings.HasPrefix(args[0], "+") {
		layout := translateDateFormat(args[0][1:])
		_, _ = fmt.Fprintln(ioCtx.Stdout, now.Format(layout))
		return 0
	}
	_, _ = fmt.Fprintln(ioCtx.Stdout, now.Format(time.RFC3339))
	return 0
}

func translateDateFormat(format string) string {
	repl := strings.NewReplacer(
		"%Y", "2006",
		"%m", "01",
		"%d", "02",
		"%H", "15",
		"%M", "04",
		"%S", "05",
		"%z", "-0700",
		"%Z", "MST",
	)
	return repl.Replace(format)
}

func cmdSeq(_ context.Context, args []string, ioCtx CommandIO) int {
	if len(args) == 0 || len(args) > 3 {
		writeErrf(ioCtx, "seq: expected START [STEP] END\n")
		return 1
	}

	toFloat := func(s string) (float64, error) {
		return strconv.ParseFloat(s, 64)
	}

	start := 1.0
	step := 1.0
	end := 1.0
	var err error

	switch len(args) {
	case 1:
		end, err = toFloat(args[0])
	case 2:
		start, err = toFloat(args[0])
		if err == nil {
			end, err = toFloat(args[1])
		}
	case 3:
		start, err = toFloat(args[0])
		if err == nil {
			step, err = toFloat(args[1])
		}
		if err == nil {
			end, err = toFloat(args[2])
		}
	}
	if err != nil || step == 0 {
		writeErrf(ioCtx, "seq: invalid numeric arguments\n")
		return 1
	}

	if step > 0 {
		for v := start; v <= end+1e-9; v += step {
			_, _ = fmt.Fprintln(ioCtx.Stdout, trimFloat(v))
		}
	} else {
		for v := start; v >= end-1e-9; v += step {
			_, _ = fmt.Fprintln(ioCtx.Stdout, trimFloat(v))
		}
	}

	return 0
}

func trimFloat(v float64) string {
	if math.Abs(v-math.Round(v)) < 1e-9 {
		return strconv.FormatInt(int64(math.Round(v)), 10)
	}
	return strconv.FormatFloat(v, 'f', -1, 64)
}

func cmdSleep(ctx context.Context, args []string, ioCtx CommandIO) int {
	if len(args) != 1 {
		writeErrf(ioCtx, "sleep: expected one duration\n")
		return 1
	}
	d, err := parseDuration(args[0])
	if err != nil {
		writeErrf(ioCtx, "sleep: %v\n", err)
		return 1
	}
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return 1
	case <-t.C:
		return 0
	}
}

func parseDuration(input string) (time.Duration, error) {
	if strings.HasSuffix(input, "ms") || strings.HasSuffix(input, "s") || strings.HasSuffix(input, "m") || strings.HasSuffix(input, "h") {
		return time.ParseDuration(input)
	}
	f, err := strconv.ParseFloat(input, 64)
	if err != nil {
		return 0, err
	}
	return time.Duration(f * float64(time.Second)), nil
}

func cmdExpr(_ context.Context, args []string, ioCtx CommandIO) int {
	if len(args) < 3 {
		writeErrf(ioCtx, "expr: expected EXPRESSION\n")
		return 1
	}

	left := args[0]
	op := args[1]
	right := args[2]

	li, lerr := strconv.Atoi(left)
	ri, rerr := strconv.Atoi(right)
	if lerr == nil && rerr == nil {
		switch op {
		case "+":
			_, _ = fmt.Fprintln(ioCtx.Stdout, li+ri)
			return 0
		case "-":
			_, _ = fmt.Fprintln(ioCtx.Stdout, li-ri)
			return 0
		case "*":
			_, _ = fmt.Fprintln(ioCtx.Stdout, li*ri)
			return 0
		case "/":
			if ri == 0 {
				writeErrf(ioCtx, "expr: division by zero\n")
				return 1
			}
			_, _ = fmt.Fprintln(ioCtx.Stdout, li/ri)
			return 0
		case "=", "==":
			if li == ri {
				_, _ = fmt.Fprintln(ioCtx.Stdout, 1)
				return 0
			}
			_, _ = fmt.Fprintln(ioCtx.Stdout, 0)
			return 1
		case "!=":
			if li != ri {
				_, _ = fmt.Fprintln(ioCtx.Stdout, 1)
				return 0
			}
			_, _ = fmt.Fprintln(ioCtx.Stdout, 0)
			return 1
		}
	}

	if op == ":" {
		if strings.Contains(left, right) {
			_, _ = fmt.Fprintln(ioCtx.Stdout, 1)
			return 0
		}
		_, _ = fmt.Fprintln(ioCtx.Stdout, 0)
		return 1
	}

	writeErrf(ioCtx, "expr: unsupported operator %s\n", op)
	return 1
}

func cmdTrue(_ context.Context, _ []string, _ CommandIO) int  { return 0 }
func cmdFalse(_ context.Context, _ []string, _ CommandIO) int { return 1 }

func cmdWhich(_ context.Context, args []string, ioCtx CommandIO) int {
	if len(args) == 0 {
		writeErrf(ioCtx, "which: missing command name\n")
		return 1
	}
	exitCode := 0
	for _, name := range args {
		if _, ok := ioCtx.Cmds[name]; ok {
			_, _ = fmt.Fprintf(ioCtx.Stdout, "/bin/%s\n", name)
			continue
		}
		writeErrf(ioCtx, "which: no %s in PATH\n", name)
		exitCode = 1
	}
	return exitCode
}

func cmdWhoami(_ context.Context, _ []string, ioCtx CommandIO) int {
	name := ioCtx.Env["USER"]
	if name == "" {
		name = "user"
	}
	_, _ = fmt.Fprintln(ioCtx.Stdout, name)
	return 0
}

func cmdHostname(_ context.Context, _ []string, ioCtx CommandIO) int {
	host := ioCtx.Env["HOSTNAME"]
	if host == "" {
		host = "gopherbox"
	}
	_, _ = fmt.Fprintln(ioCtx.Stdout, host)
	return 0
}

func cmdTee(_ context.Context, args []string, ioCtx CommandIO) int {
	appendMode := false
	targets := make([]string, 0, len(args))
	for _, arg := range args {
		if arg == "-a" {
			appendMode = true
			continue
		}
		targets = append(targets, arg)
	}

	outputs := []io.Writer{ioCtx.Stdout}
	files := []io.Closer{}
	for _, target := range targets {
		abs := resolvePath(ioCtx.Cwd, target)
		flags := os.O_CREATE | os.O_WRONLY
		if appendMode {
			flags |= os.O_APPEND
		} else {
			flags |= os.O_TRUNC
		}
		f, err := ioCtx.Fs.OpenFile(abs, flags, 0o644)
		if err != nil {
			writeErrf(ioCtx, "tee: %s: %v\n", target, err)
			for _, c := range files {
				_ = c.Close()
			}
			return 1
		}
		files = append(files, f)
		outputs = append(outputs, f)
	}
	defer func() {
		for _, c := range files {
			_ = c.Close()
		}
	}()

	_, err := io.Copy(io.MultiWriter(outputs...), ioCtx.Stdin)
	if err != nil {
		writeErrf(ioCtx, "tee: %v\n", err)
		return 1
	}
	return 0
}

func cmdChmod(_ context.Context, args []string, ioCtx CommandIO) int {
	if len(args) < 2 {
		writeErrf(ioCtx, "chmod: missing operand\n")
		return 1
	}
	mode64, err := strconv.ParseUint(args[0], 8, 32)
	if err != nil {
		writeErrf(ioCtx, "chmod: invalid mode: %s\n", args[0])
		return 1
	}
	mode := os.FileMode(mode64)
	exitCode := 0
	for _, target := range args[1:] {
		abs := resolvePath(ioCtx.Cwd, target)
		if err := ioCtx.Fs.Chmod(abs, mode); err != nil {
			writeErrf(ioCtx, "chmod: %s: %v\n", target, err)
			exitCode = 1
		}
	}
	return exitCode
}

func cmdTime(ctx context.Context, args []string, ioCtx CommandIO) int {
	if len(args) == 0 {
		writeErrf(ioCtx, "time: missing command\n")
		return 1
	}
	start := time.Now()
	exitCode := runSubcommand(ctx, args, ioCtx)
	elapsed := time.Since(start)
	_, _ = fmt.Fprintf(ioCtx.Stderr, "real %.3fs\n", elapsed.Seconds())
	return exitCode
}

func cmdTimeout(ctx context.Context, args []string, ioCtx CommandIO) int {
	if len(args) < 2 {
		writeErrf(ioCtx, "timeout: expected DURATION COMMAND ...\n")
		return 1
	}
	dur, err := parseDuration(args[0])
	if err != nil {
		writeErrf(ioCtx, "timeout: %v\n", err)
		return 1
	}
	tctx, cancel := context.WithTimeout(ctx, dur)
	defer cancel()

	exitCode := runSubcommand(tctx, args[1:], ioCtx)
	if errors.Is(tctx.Err(), context.DeadlineExceeded) {
		return 124
	}
	return exitCode
}
