package commands

import (
	"context"
	"errors"
	"fmt"
	"io"
	iofs "io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"syscall"
	"time"
	"unicode/utf8"

	"github.com/spf13/afero"
)

func cmdCat(_ context.Context, args []string, ioCtx CommandIO) int {
	if len(args) == 0 {
		return copyStream(ioCtx.Stdout, ioCtx.Stdin)
	}

	exitCode := 0
	for _, arg := range args {
		if arg == "-" {
			if copyStream(ioCtx.Stdout, ioCtx.Stdin) != 0 {
				exitCode = 1
			}
			continue
		}
		abs := resolvePath(ioCtx.Cwd, arg)
		f, err := ioCtx.Fs.Open(abs)
		if err != nil {
			writeErrf(ioCtx, "cat: %s: %s\n", arg, fileOpError(err))
			exitCode = 1
			continue
		}
		_, err = io.Copy(ioCtx.Stdout, f)
		_ = f.Close()
		if err != nil {
			writeErrf(ioCtx, "cat: %s: %s\n", arg, fileOpError(err))
			exitCode = 1
		}
	}
	return exitCode
}

func cmdCp(_ context.Context, args []string, ioCtx CommandIO) int {
	recursive := false
	filtered := make([]string, 0, len(args))
	for _, arg := range args {
		switch arg {
		case "-r", "-R":
			recursive = true
		default:
			filtered = append(filtered, arg)
		}
	}
	if len(filtered) < 2 {
		writeErrf(ioCtx, "cp: missing source or destination\n")
		return 1
	}

	sources := filtered[:len(filtered)-1]
	dstArg := filtered[len(filtered)-1]
	dstAbs := resolvePath(ioCtx.Cwd, dstArg)

	dstInfo, dstErr := ioCtx.Fs.Stat(dstAbs)
	dstIsDir := dstErr == nil && dstInfo.IsDir()

	if len(sources) > 1 && !dstIsDir {
		writeErrf(ioCtx, "cp: target '%s' is not a directory\n", dstArg)
		return 1
	}

	exitCode := 0
	for _, srcArg := range sources {
		srcAbs := resolvePath(ioCtx.Cwd, srcArg)
		srcInfo, err := ioCtx.Fs.Stat(srcAbs)
		if err != nil {
			writeErrf(ioCtx, "cp: cannot stat '%s': %s\n", srcArg, fileOpError(err))
			exitCode = 1
			continue
		}

		targetAbs := dstAbs
		if dstIsDir {
			targetAbs = resolvePath(dstAbs, filepath.Base(srcAbs))
		}

		if srcInfo.IsDir() {
			if !recursive {
				writeErrf(ioCtx, "cp: -r not specified; omitting directory '%s'\n", srcArg)
				exitCode = 1
				continue
			}
			if err := copyDir(ioCtx.Fs, srcAbs, targetAbs); err != nil {
				writeErrf(ioCtx, "cp: cannot copy '%s': %s\n", srcArg, fileOpError(err))
				exitCode = 1
			}
			continue
		}

		if err := copyFile(ioCtx.Fs, srcAbs, targetAbs); err != nil {
			writeErrf(ioCtx, "cp: cannot copy '%s': %s\n", srcArg, fileOpError(err))
			exitCode = 1
		}
	}

	return exitCode
}

func copyDir(fs afero.Fs, src, dst string) error {
	info, err := fs.Stat(src)
	if err != nil {
		return err
	}
	if !info.IsDir() {
		return copyFile(fs, src, dst)
	}

	if err := fs.MkdirAll(dst, info.Mode().Perm()); err != nil {
		return err
	}

	entries, err := afero.ReadDir(fs, src)
	if err != nil {
		return err
	}

	for _, entry := range entries {
		srcChild := filepath.Join(src, entry.Name())
		dstChild := filepath.Join(dst, entry.Name())
		if entry.IsDir() {
			if err := copyDir(fs, srcChild, dstChild); err != nil {
				return err
			}
			continue
		}
		if err := copyFile(fs, srcChild, dstChild); err != nil {
			return err
		}
	}

	return nil
}

func copyFile(fs afero.Fs, src, dst string) error {
	in, err := fs.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()

	info, err := in.Stat()
	if err != nil {
		return err
	}

	if err := fs.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return err
	}

	out, err := fs.OpenFile(dst, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, info.Mode().Perm())
	if err != nil {
		return err
	}
	defer out.Close()

	_, err = io.Copy(out, in)
	return err
}

func cmdLs(_ context.Context, args []string, ioCtx CommandIO) int {
	showAll := false
	longFmt := false
	targets := []string{}

	for _, arg := range args {
		if strings.HasPrefix(arg, "-") {
			for _, flag := range arg[1:] {
				switch flag {
				case 'a':
					showAll = true
				case 'l':
					longFmt = true
				}
			}
			continue
		}
		targets = append(targets, arg)
	}

	if len(targets) == 0 {
		targets = []string{"."}
	}

	exitCode := 0
	for i, target := range targets {
		abs := resolvePath(ioCtx.Cwd, target)
		info, err := ioCtx.Fs.Stat(abs)
		if err != nil {
			writeErrf(ioCtx, "ls: cannot access '%s': %s\n", target, fileOpError(err))
			exitCode = 1
			continue
		}

		if len(targets) > 1 {
			if i > 0 {
				_, _ = fmt.Fprintln(ioCtx.Stdout)
			}
			_, _ = fmt.Fprintf(ioCtx.Stdout, "%s:\n", target)
		}

		if !info.IsDir() {
			if longFmt {
				_, _ = fmt.Fprintf(ioCtx.Stdout, "%s %8d %s\n", info.Mode().String(), info.Size(), filepath.Base(abs))
			} else {
				_, _ = fmt.Fprintln(ioCtx.Stdout, filepath.Base(abs))
			}
			continue
		}

		entries, err := afero.ReadDir(ioCtx.Fs, abs)
		if err != nil {
			writeErrf(ioCtx, "ls: %s: %s\n", target, fileOpError(err))
			exitCode = 1
			continue
		}
		if showAll {
			if longFmt {
				_, _ = fmt.Fprintf(ioCtx.Stdout, "%s %8d %s\n", info.Mode().String(), info.Size(), ".")
				parentInfo := info
				if pInfo, pErr := ioCtx.Fs.Stat(resolvePath(abs, "..")); pErr == nil {
					parentInfo = pInfo
				}
				_, _ = fmt.Fprintf(ioCtx.Stdout, "%s %8d %s\n", parentInfo.Mode().String(), parentInfo.Size(), "..")
			} else {
				_, _ = fmt.Fprintln(ioCtx.Stdout, ".")
				_, _ = fmt.Fprintln(ioCtx.Stdout, "..")
			}
		}
		for _, entry := range entries {
			name := entry.Name()
			if !showAll && strings.HasPrefix(name, ".") {
				continue
			}
			if longFmt {
				_, _ = fmt.Fprintf(ioCtx.Stdout, "%s %8d %s\n", entry.Mode().String(), entry.Size(), name)
			} else {
				_, _ = fmt.Fprintln(ioCtx.Stdout, name)
			}
		}
	}

	return exitCode
}

func cmdMkdir(_ context.Context, args []string, ioCtx CommandIO) int {
	parents := false
	targets := []string{}
	for _, arg := range args {
		if arg == "-p" {
			parents = true
			continue
		}
		targets = append(targets, arg)
	}
	if len(targets) == 0 {
		writeErrf(ioCtx, "mkdir: missing operand\n")
		return 1
	}

	exitCode := 0
	for _, target := range targets {
		abs := resolvePath(ioCtx.Cwd, target)
		var err error
		if parents {
			err = ioCtx.Fs.MkdirAll(abs, 0o755)
		} else {
			err = ioCtx.Fs.Mkdir(abs, 0o755)
		}
		if err != nil {
			writeErrf(ioCtx, "mkdir: %s: %s\n", target, fileOpError(err))
			exitCode = 1
		}
	}
	return exitCode
}

func cmdMv(_ context.Context, args []string, ioCtx CommandIO) int {
	if len(args) < 2 {
		writeErrf(ioCtx, "mv: missing source or destination\n")
		return 1
	}

	sources := args[:len(args)-1]
	dstAbs := resolvePath(ioCtx.Cwd, args[len(args)-1])
	dstInfo, dstErr := ioCtx.Fs.Stat(dstAbs)
	dstIsDir := dstErr == nil && dstInfo.IsDir()

	if len(sources) > 1 && !dstIsDir {
		writeErrf(ioCtx, "mv: target '%s' is not a directory\n", args[len(args)-1])
		return 1
	}

	exitCode := 0
	for _, srcArg := range sources {
		srcAbs := resolvePath(ioCtx.Cwd, srcArg)
		target := dstAbs
		if dstIsDir {
			target = filepath.Join(dstAbs, filepath.Base(srcAbs))
		}
		if err := ioCtx.Fs.Rename(srcAbs, target); err != nil {
			writeErrf(ioCtx, "mv: %s: %s\n", srcArg, fileOpError(err))
			exitCode = 1
		}
	}

	return exitCode
}

func cmdRm(_ context.Context, args []string, ioCtx CommandIO) int {
	recursive := false
	force := false
	targets := []string{}
	for _, arg := range args {
		switch arg {
		case "-r", "-R", "-rf", "-fr":
			recursive = true
			if strings.Contains(arg, "f") {
				force = true
			}
		case "-f":
			force = true
		default:
			targets = append(targets, arg)
		}
	}

	if len(targets) == 0 {
		if force {
			return 0
		}
		writeErrf(ioCtx, "rm: missing operand\n")
		return 1
	}

	exitCode := 0
	for _, target := range targets {
		abs := resolvePath(ioCtx.Cwd, target)
		info, err := ioCtx.Fs.Stat(abs)
		if err != nil {
			if !force {
				writeErrf(ioCtx, "rm: cannot remove '%s': %s\n", target, fileOpError(err))
				exitCode = 1
			}
			continue
		}

		if info.IsDir() && !recursive {
			writeErrf(ioCtx, "rm: cannot remove '%s': is a directory\n", target)
			exitCode = 1
			continue
		}

		if info.IsDir() {
			err = ioCtx.Fs.RemoveAll(abs)
		} else {
			err = ioCtx.Fs.Remove(abs)
		}
		if err != nil && !force {
			writeErrf(ioCtx, "rm: cannot remove '%s': %s\n", target, fileOpError(err))
			exitCode = 1
		}
	}

	return exitCode
}

func cmdRmdir(_ context.Context, args []string, ioCtx CommandIO) int {
	if len(args) == 0 {
		writeErrf(ioCtx, "rmdir: missing operand\n")
		return 1
	}
	exitCode := 0
	for _, arg := range args {
		abs := resolvePath(ioCtx.Cwd, arg)
		if err := ioCtx.Fs.Remove(abs); err != nil {
			writeErrf(ioCtx, "rmdir: %s: %s\n", arg, fileOpError(err))
			exitCode = 1
		}
	}
	return exitCode
}

func cmdTouch(_ context.Context, args []string, ioCtx CommandIO) int {
	if len(args) == 0 {
		writeErrf(ioCtx, "touch: missing file operand\n")
		return 1
	}

	now := time.Now()
	exitCode := 0
	for _, arg := range args {
		abs := resolvePath(ioCtx.Cwd, arg)
		if _, err := ioCtx.Fs.Stat(abs); err != nil {
			if err := afero.WriteFile(ioCtx.Fs, abs, nil, 0o644); err != nil {
				writeErrf(ioCtx, "touch: %s: %s\n", arg, fileOpError(err))
				exitCode = 1
			}
			continue
		}
		if err := ioCtx.Fs.Chtimes(abs, now, now); err != nil {
			// Some afero backends do not support Chtimes; tolerate this.
			if err := afero.WriteFile(ioCtx.Fs, abs, mustReadFile(ioCtx.Fs, abs), 0o644); err != nil {
				writeErrf(ioCtx, "touch: %s: %s\n", arg, fileOpError(err))
				exitCode = 1
			}
		}
	}

	return exitCode
}

func mustReadFile(fs afero.Fs, path string) []byte {
	b, _ := afero.ReadFile(fs, path)
	return b
}

func cmdLn(_ context.Context, args []string, ioCtx CommandIO) int {
	if len(args) < 2 {
		writeErrf(ioCtx, "ln: missing target and link name\n")
		return 1
	}

	symbolic := false
	filtered := make([]string, 0, len(args))
	for _, arg := range args {
		if arg == "-s" {
			symbolic = true
			continue
		}
		filtered = append(filtered, arg)
	}
	if len(filtered) != 2 {
		writeErrf(ioCtx, "ln: requires target and link name\n")
		return 1
	}

	src := filtered[0]
	linkAbs := resolvePath(ioCtx.Cwd, filtered[1])

	if symbolic {
		symlinkTarget := resolvePath(ioCtx.Cwd, src)
		if linker, ok := ioCtx.Fs.(afero.Linker); ok {
			if err := linker.SymlinkIfPossible(symlinkTarget, linkAbs); err != nil {
				writeErrf(ioCtx, "ln: %s\n", fileOpError(err))
				return 1
			}
			return 0
		}
		writeErrf(ioCtx, "ln: symbolic links not supported by filesystem\n")
		return 1
	}

	// Hard links are emulated as file copies.
	srcAbs := resolvePath(ioCtx.Cwd, src)
	if err := copyFile(ioCtx.Fs, srcAbs, linkAbs); err != nil {
		writeErrf(ioCtx, "ln: %s\n", fileOpError(err))
		return 1
	}
	return 0
}

func cmdStat(_ context.Context, args []string, ioCtx CommandIO) int {
	if len(args) == 0 {
		writeErrf(ioCtx, "stat: missing operand\n")
		return 1
	}

	exitCode := 0
	for _, arg := range args {
		abs := resolvePath(ioCtx.Cwd, arg)
		info, err := ioCtx.Fs.Stat(abs)
		if err != nil {
			writeErrf(ioCtx, "stat: cannot stat '%s': %s\n", arg, fileOpError(err))
			exitCode = 1
			continue
		}
		_, _ = fmt.Fprintf(ioCtx.Stdout, "File: %s\n", arg)
		_, _ = fmt.Fprintf(ioCtx.Stdout, "Size: %d\n", info.Size())
		_, _ = fmt.Fprintf(ioCtx.Stdout, "Mode: %s\n", info.Mode())
		_, _ = fmt.Fprintf(ioCtx.Stdout, "Modified: %s\n", info.ModTime().Format(time.RFC3339))
	}
	return exitCode
}

func cmdReadlink(_ context.Context, args []string, ioCtx CommandIO) int {
	if len(args) != 1 {
		writeErrf(ioCtx, "readlink: expected exactly one path\n")
		return 1
	}
	if lr, ok := ioCtx.Fs.(afero.LinkReader); ok {
		target, err := lr.ReadlinkIfPossible(resolvePath(ioCtx.Cwd, args[0]))
		if err != nil {
			if errors.Is(err, iofs.ErrNotExist) {
				writeErrf(ioCtx, "readlink: %s: no such file or directory\n", args[0])
				return 1
			}
			writeErrf(ioCtx, "readlink: %s: %s\n", args[0], fileOpError(err))
			return 1
		}
		target = mapReadlinkTarget(ioCtx.Fs, target)
		_, _ = fmt.Fprintln(ioCtx.Stdout, target)
		return 0
	}
	writeErrf(ioCtx, "readlink: symlink read not supported\n")
	return 1
}

func cmdTree(_ context.Context, args []string, ioCtx CommandIO) int {
	root := "."
	if len(args) > 0 {
		root = args[0]
	}
	rootAbs := resolvePath(ioCtx.Cwd, root)
	if _, err := ioCtx.Fs.Stat(rootAbs); err != nil {
		writeErrf(ioCtx, "tree: %s: %s\n", root, fileOpError(err))
		return 1
	}

	_, _ = fmt.Fprintln(ioCtx.Stdout, root)
	return printTree(ioCtx.Fs, ioCtx.Stdout, rootAbs, "")
}

func printTree(fs afero.Fs, out io.Writer, root, prefix string) int {
	entries, err := afero.ReadDir(fs, root)
	if err != nil {
		return 1
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].Name() < entries[j].Name() })
	for i, entry := range entries {
		connector := "|-- "
		nextPrefix := prefix + "|   "
		if i == len(entries)-1 {
			connector = "`-- "
			nextPrefix = prefix + "    "
		}
		_, _ = fmt.Fprintf(out, "%s%s%s\n", prefix, connector, entry.Name())
		if entry.IsDir() {
			if printTree(fs, out, filepath.Join(root, entry.Name()), nextPrefix) != 0 {
				return 1
			}
		}
	}
	return 0
}

func cmdFile(_ context.Context, args []string, ioCtx CommandIO) int {
	if len(args) == 0 {
		writeErrf(ioCtx, "file: missing operand\n")
		return 1
	}
	exitCode := 0
	for _, arg := range args {
		abs := resolvePath(ioCtx.Cwd, arg)
		info, err := ioCtx.Fs.Stat(abs)
		if err != nil {
			writeErrf(ioCtx, "file: %s: %s\n", arg, fileOpError(err))
			exitCode = 1
			continue
		}

		kind := "data"
		switch {
		case info.IsDir():
			kind = "directory"
		default:
			f, err := ioCtx.Fs.Open(abs)
			if err == nil {
				buf := make([]byte, 512)
				n, _ := f.Read(buf)
				_ = f.Close()
				if n > 0 && utf8.Valid(buf[:n]) && isMostlyText(buf[:n]) {
					kind = "text"
				} else {
					kind = "binary"
				}
			}
		}

		_, _ = fmt.Fprintf(ioCtx.Stdout, "%s: %s\n", arg, kind)
	}
	return exitCode
}

func isMostlyText(b []byte) bool {
	if len(b) == 0 {
		return true
	}
	printable := 0
	for _, c := range b {
		if c == '\n' || c == '\r' || c == '\t' || (c >= 32 && c <= 126) {
			printable++
		}
	}
	return float64(printable)/float64(len(b)) > 0.8
}

func mapReadlinkTarget(fs afero.Fs, target string) string {
	if !filepath.IsAbs(target) {
		return target
	}

	type realPathFs interface {
		RealPath(name string) (string, error)
	}

	bp, ok := fs.(realPathFs)
	if !ok {
		return target
	}

	root, err := bp.RealPath("/")
	if err != nil {
		return target
	}

	cleanRoot := filepath.Clean(root)
	cleanTarget := filepath.Clean(target)
	if cleanTarget == cleanRoot {
		return "/"
	}
	prefix := cleanRoot + string(filepath.Separator)
	if !strings.HasPrefix(cleanTarget, prefix) {
		return target
	}

	return cleanAbsolute("/" + strings.TrimPrefix(cleanTarget, prefix))
}

func fileOpError(err error) string {
	if err == nil {
		return ""
	}

	var pathErr *os.PathError
	if errors.As(err, &pathErr) {
		return fileOpError(pathErr.Err)
	}
	var linkErr *os.LinkError
	if errors.As(err, &linkErr) {
		return fileOpError(linkErr.Err)
	}
	var syscallErr *os.SyscallError
	if errors.As(err, &syscallErr) {
		return fileOpError(syscallErr.Err)
	}

	switch {
	case errors.Is(err, iofs.ErrNotExist):
		return "no such file or directory"
	case errors.Is(err, iofs.ErrExist):
		return "file exists"
	case errors.Is(err, iofs.ErrPermission):
		return "permission denied"
	case errors.Is(err, syscall.ENOTEMPTY):
		return "directory not empty"
	case errors.Is(err, syscall.EISDIR):
		return "is a directory"
	case errors.Is(err, syscall.ENOTDIR):
		return "not a directory"
	default:
		return err.Error()
	}
}
