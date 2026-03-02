package commands

import (
	"context"
	"fmt"
	"io"
	"path/filepath"
	"sort"
	"strings"

	"github.com/spf13/afero"
)

// CommandFunc is the function signature for a gopherbox command implementation.
type CommandFunc func(ctx context.Context, args []string, io CommandIO) int

// NetworkPolicy allows command implementations to validate network access.
type NetworkPolicy interface {
	MethodAllowed(method string) bool
	URLAllowed(url string) bool
}

// CommandIO provides command execution context and IO bindings.
type CommandIO struct {
	Stdin   io.Reader
	Stdout  io.Writer
	Stderr  io.Writer
	Fs      afero.Fs
	Env     map[string]string
	Cwd     string
	Network NetworkPolicy
	Cmds    map[string]CommandFunc
}

// DefaultCommands returns the built-in command set.
func DefaultCommands() map[string]CommandFunc {
	cmds := map[string]CommandFunc{
		// file operations
		"cat":      cmdCat,
		"cp":       cmdCp,
		"ls":       cmdLs,
		"mkdir":    cmdMkdir,
		"mv":       cmdMv,
		"rm":       cmdRm,
		"rmdir":    cmdRmdir,
		"touch":    cmdTouch,
		"ln":       cmdLn,
		"stat":     cmdStat,
		"readlink": cmdReadlink,
		"tree":     cmdTree,
		"file":     cmdFile,

		// text processing
		"grep":     cmdGrep,
		"egrep":    cmdEgrep,
		"fgrep":    cmdFgrep,
		"sed":      cmdSed,
		"head":     cmdHead,
		"tail":     cmdTail,
		"sort":     cmdSort,
		"uniq":     cmdUniq,
		"wc":       cmdWc,
		"cut":      cmdCut,
		"tr":       cmdTr,
		"rev":      cmdRev,
		"tac":      cmdTac,
		"paste":    cmdPaste,
		"fold":     cmdFold,
		"nl":       cmdNl,
		"expand":   cmdExpand,
		"unexpand": cmdUnexpand,
		"column":   cmdColumn,
		"comm":     cmdComm,
		"join":     cmdJoin,
		"diff":     cmdDiff,
		"strings":  cmdStrings,

		// data and search
		"awk":       cmdAwk,
		"find":      cmdFind,
		"xargs":     cmdXargs,
		"jq":        cmdJQ,
		"base64":    cmdBase64,
		"md5sum":    cmdMD5Sum,
		"sha1sum":   cmdSHA1Sum,
		"sha256sum": cmdSHA256Sum,

		// archives and network
		"tar":    cmdTar,
		"gzip":   cmdGzip,
		"gunzip": cmdGunzip,
		"zcat":   cmdZcat,
		"curl":   cmdCurl,

		// shell utilities
		"echo":     cmdEcho,
		"printf":   cmdPrintf,
		"env":      cmdEnv,
		"printenv": cmdPrintenv,
		"pwd":      cmdPwd,
		"cd":       cmdCd,
		"basename": cmdBasename,
		"dirname":  cmdDirname,
		"du":       cmdDu,
		"date":     cmdDate,
		"seq":      cmdSeq,
		"sleep":    cmdSleep,
		"expr":     cmdExpr,
		"true":     cmdTrue,
		"false":    cmdFalse,
		"which":    cmdWhich,
		"whoami":   cmdWhoami,
		"hostname": cmdHostname,
		"tee":      cmdTee,
		"chmod":    cmdChmod,
		"time":     cmdTime,
		"timeout":  cmdTimeout,
	}
	return cmds
}

func writeErrf(ioCtx CommandIO, format string, args ...any) {
	_, _ = fmt.Fprintf(ioCtx.Stderr, format, args...)
}

func runSubcommand(ctx context.Context, argv []string, ioCtx CommandIO) int {
	if len(argv) == 0 {
		return 0
	}
	cmd, ok := ioCtx.Cmds[argv[0]]
	if !ok {
		writeErrf(ioCtx, "%s: command not found\n", argv[0])
		return 127
	}
	return cmd(ctx, argv[1:], ioCtx)
}

func copyStream(dst io.Writer, src io.Reader) int {
	if _, err := io.Copy(dst, src); err != nil {
		return 1
	}
	return 0
}

func splitKeepNonEmpty(s string) []string {
	fields := strings.Fields(s)
	out := make([]string, 0, len(fields))
	for _, f := range fields {
		if f != "" {
			out = append(out, f)
		}
	}
	return out
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
