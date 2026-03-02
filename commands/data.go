package commands

import (
	"bytes"
	"context"
	"crypto/md5"
	"crypto/sha1"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"hash"
	"io"
	iofs "io/fs"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"unicode"

	awkinterp "github.com/benhoyt/goawk/interp"
	awkparser "github.com/benhoyt/goawk/parser"
	"github.com/itchyny/gojq"
	"github.com/spf13/afero"
)

func cmdAwk(_ context.Context, args []string, ioCtx CommandIO) int {
	if len(args) == 0 {
		writeErrf(ioCtx, "awk: missing program\n")
		return 2
	}

	script := args[0]
	files := args[1:]
	prog, err := awkparser.ParseProgram([]byte(script), nil)
	if err != nil {
		writeErrf(ioCtx, "awk: %v\n", err)
		return 2
	}

	var input io.Reader
	if len(files) == 0 {
		input = ioCtx.Stdin
	} else {
		readers := make([]io.Reader, 0, len(files))
		for _, f := range files {
			if f == "-" {
				data, readErr := io.ReadAll(ioCtx.Stdin)
				if readErr != nil {
					writeErrf(ioCtx, "awk: stdin: %s\n", fileOpError(readErr))
					return 2
				}
				readers = append(readers, bytes.NewReader(data))
			} else {
				b, readErr := afero.ReadFile(ioCtx.Fs, resolvePath(ioCtx.Cwd, f))
				if readErr != nil {
					writeErrf(ioCtx, "awk: can't open file %s\n source line number 1\n", f)
					return 2
				}
				readers = append(readers, bytes.NewReader(b))
			}
		}
		input = io.MultiReader(readers...)
	}

	_, err = awkinterp.ExecProgram(prog, &awkinterp.Config{
		Stdin:  input,
		Output: ioCtx.Stdout,
		Error:  ioCtx.Stderr,
	})
	if err != nil {
		writeErrf(ioCtx, "awk: %v\n", err)
		return 2
	}

	return 0
}

func cmdFind(_ context.Context, args []string, ioCtx CommandIO) int {
	start := "."
	namePattern := ""
	findType := ""

	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "-name":
			if i+1 < len(args) {
				namePattern = args[i+1]
				i++
			}
		case "-type":
			if i+1 < len(args) {
				findType = args[i+1]
				i++
			}
		default:
			if !strings.HasPrefix(args[i], "-") {
				start = args[i]
			}
		}
	}

	startAbs := resolvePath(ioCtx.Cwd, start)
	if _, err := ioCtx.Fs.Stat(startAbs); err != nil {
		if errors.Is(err, iofs.ErrNotExist) {
			writeErrf(ioCtx, "find: %s: No such file or directory\n", start)
			return 1
		}
		writeErrf(ioCtx, "find: %s: %s\n", start, fileOpError(err))
		return 1
	}

	exitCode := 0
	err := afero.Walk(ioCtx.Fs, startAbs, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			displayPath := path
			if rel, relErr := filepath.Rel(ioCtx.Cwd, path); relErr == nil && !strings.HasPrefix(rel, "..") {
				displayPath = rel
			}
			writeErrf(ioCtx, "find: %s: %s\n", displayPath, fileOpError(err))
			exitCode = 1
			return nil
		}

		if namePattern != "" {
			ok, matchErr := filepath.Match(namePattern, filepath.Base(path))
			if matchErr != nil || !ok {
				if info.IsDir() {
					return nil
				}
				return nil
			}
		}

		if findType == "f" && info.IsDir() {
			return nil
		}
		if findType == "d" && !info.IsDir() {
			return nil
		}

		rel, relErr := filepath.Rel(ioCtx.Cwd, path)
		if relErr != nil || strings.HasPrefix(rel, "..") {
			rel = path
		}
		if rel == "" {
			rel = "."
		}
		_, _ = fmt.Fprintln(ioCtx.Stdout, rel)
		return nil
	})
	if err != nil {
		writeErrf(ioCtx, "find: %s\n", fileOpError(err))
		return 1
	}
	return exitCode
}

func cmdXargs(ctx context.Context, args []string, ioCtx CommandIO) int {
	maxArgs := 0
	filtered := []string{}
	for i := 0; i < len(args); i++ {
		if args[i] == "-n" || strings.HasPrefix(args[i], "-n") {
			val := ""
			if args[i] == "-n" {
				if i+1 >= len(args) {
					writeErrf(ioCtx, "xargs: option requires an argument -- n\n")
					return 1
				}
				val = args[i+1]
				i++
			} else {
				val = args[i][2:]
			}
			n, err := strconv.Atoi(val)
			if err != nil || n <= 0 {
				writeErrf(ioCtx, "xargs: invalid maximum args value: %s\n", val)
				return 1
			}
			maxArgs = n
			continue
		}
		filtered = append(filtered, args[i])
	}
	if len(filtered) == 0 {
		filtered = []string{"echo"}
	}
	if _, ok := ioCtx.Cmds[filtered[0]]; !ok {
		writeErrf(ioCtx, "xargs: %s: No such file or directory\n", filtered[0])
		return 127
	}

	data, err := io.ReadAll(ioCtx.Stdin)
	if err != nil {
		writeErrf(ioCtx, "xargs: %s\n", fileOpError(err))
		return 1
	}
	tokens, err := parseXargsTokens(string(data))
	if err != nil {
		writeErrf(ioCtx, "xargs: %v\n", err)
		return 1
	}
	if len(tokens) == 0 {
		return 0
	}

	if maxArgs <= 0 {
		maxArgs = len(tokens)
	}

	exitCode := 0
	for i := 0; i < len(tokens); i += maxArgs {
		end := i + maxArgs
		if end > len(tokens) {
			end = len(tokens)
		}
		cmdArgs := append(append([]string{}, filtered...), tokens[i:end]...)
		code := runSubcommand(ctx, cmdArgs, ioCtx)
		if code != 0 {
			exitCode = code
		}
	}

	return exitCode
}

func cmdJQ(_ context.Context, args []string, ioCtx CommandIO) int {
	if len(args) == 0 {
		writeErrf(ioCtx, "jq: missing filter\n")
		return 2
	}
	query, err := gojq.Parse(args[0])
	if err != nil {
		writeErrf(ioCtx, "jq: %v\n", err)
		return 3
	}

	inputs := make([]namedInput, 0, len(args)-1)
	hadInputError := false
	if len(args) == 1 {
		inputs = append(inputs, namedInput{name: "-", data: mustReadAll(ioCtx.Stdin)})
	} else {
		for _, f := range args[1:] {
			if f == "-" {
				inputs = append(inputs, namedInput{name: "-", data: mustReadAll(ioCtx.Stdin)})
				continue
			}
			data, readErr := afero.ReadFile(ioCtx.Fs, resolvePath(ioCtx.Cwd, f))
			if readErr != nil {
				if errors.Is(readErr, iofs.ErrNotExist) {
					writeErrf(ioCtx, "jq: error: Could not open file %s: No such file or directory\n", f)
				} else {
					writeErrf(ioCtx, "jq: error: Could not open file %s: %s\n", f, fileOpError(readErr))
				}
				hadInputError = true
				continue
			}
			inputs = append(inputs, namedInput{name: f, data: data})
		}
	}
	if len(inputs) == 0 {
		if hadInputError {
			return 2
		}
		return 0
	}

	encoder := json.NewEncoder(ioCtx.Stdout)
	encoder.SetEscapeHTML(false)

	exitCode := 0
	for _, input := range inputs {
		dec := json.NewDecoder(bytes.NewReader(input.data))
		for {
			var value any
			if err := dec.Decode(&value); err != nil {
				if err == io.EOF {
					break
				}
				writeErrf(ioCtx, "jq: %s: %v\n", input.name, err)
				exitCode = 5
				break
			}

			iter := query.Run(value)
			for {
				v, ok := iter.Next()
				if !ok {
					break
				}
				if err, ok := v.(error); ok {
					writeErrf(ioCtx, "jq: %v\n", err)
					exitCode = 5
					continue
				}
				if err := encoder.Encode(v); err != nil {
					writeErrf(ioCtx, "jq: %v\n", err)
					exitCode = 5
				}
			}
		}
	}
	if hadInputError {
		return 2
	}

	return exitCode
}

func mustReadAll(r io.Reader) []byte {
	b, _ := io.ReadAll(r)
	return b
}

func cmdBase64(_ context.Context, args []string, ioCtx CommandIO) int {
	decode := false
	files := []string{}
	for _, arg := range args {
		if arg == "-d" || arg == "--decode" {
			decode = true
			continue
		}
		files = append(files, arg)
	}

	inputs, readExit := readNamedInputs(files, ioCtx)
	if len(inputs) == 0 {
		inputs = []namedInput{{name: "-", data: mustReadAll(ioCtx.Stdin)}}
	}

	exitCode := readExit
	for _, in := range inputs {
		if decode {
			decoded, err := base64.StdEncoding.DecodeString(strings.TrimSpace(string(in.data)))
			if err != nil {
				writeErrf(ioCtx, "base64: %v\n", err)
				exitCode = 1
				continue
			}
			_, _ = ioCtx.Stdout.Write(decoded)
			continue
		}
		enc := base64.StdEncoding.EncodeToString(in.data)
		_, _ = fmt.Fprintln(ioCtx.Stdout, enc)
	}
	return exitCode
}

func cmdMD5Sum(_ context.Context, args []string, ioCtx CommandIO) int {
	return hashCommand("md5sum", args, ioCtx, md5.New)
}

func cmdSHA1Sum(_ context.Context, args []string, ioCtx CommandIO) int {
	return hashCommand("sha1sum", args, ioCtx, sha1.New)
}

func cmdSHA256Sum(_ context.Context, args []string, ioCtx CommandIO) int {
	return hashCommand("sha256sum", args, ioCtx, sha256.New)
}

func hashCommand(cmdName string, args []string, ioCtx CommandIO, newHash func() hash.Hash) int {
	if len(args) == 0 {
		h := newHash()
		if _, err := io.Copy(h, ioCtx.Stdin); err != nil {
			writeErrf(ioCtx, "%s: %s\n", cmdName, fileOpError(err))
			return 1
		}
		_, _ = fmt.Fprintf(ioCtx.Stdout, "%s  -\n", hex.EncodeToString(h.Sum(nil)))
		return 0
	}

	exitCode := 0
	for _, arg := range args {
		abs := resolvePath(ioCtx.Cwd, arg)
		f, err := ioCtx.Fs.Open(abs)
		if err != nil {
			if errors.Is(err, iofs.ErrNotExist) {
				writeErrf(ioCtx, "%s: %s: No such file or directory\n", cmdName, arg)
			} else {
				writeErrf(ioCtx, "%s: %s: %s\n", cmdName, arg, fileOpError(err))
			}
			exitCode = 1
			continue
		}
		h := newHash()
		if _, err := io.Copy(h, f); err != nil {
			writeErrf(ioCtx, "%s: %s: %s\n", cmdName, arg, fileOpError(err))
			exitCode = 1
		}
		_ = f.Close()
		_, _ = fmt.Fprintf(ioCtx.Stdout, "%s  %s\n", hex.EncodeToString(h.Sum(nil)), arg)
	}
	return exitCode
}

func parseXargsTokens(input string) ([]string, error) {
	tokens := make([]string, 0)
	current := strings.Builder{}
	inSingle := false
	inDouble := false
	escaped := false

	flush := func() {
		if current.Len() == 0 {
			return
		}
		tokens = append(tokens, current.String())
		current.Reset()
	}

	for _, r := range input {
		if escaped {
			current.WriteRune(r)
			escaped = false
			continue
		}

		if inSingle {
			if r == '\'' {
				inSingle = false
				continue
			}
			current.WriteRune(r)
			continue
		}

		if inDouble {
			if r == '"' {
				inDouble = false
				continue
			}
			if r == '\\' {
				escaped = true
				continue
			}
			current.WriteRune(r)
			continue
		}

		switch {
		case unicode.IsSpace(r):
			flush()
		case r == '\\':
			escaped = true
		case r == '\'':
			inSingle = true
		case r == '"':
			inDouble = true
		default:
			current.WriteRune(r)
		}
	}

	if escaped || inSingle || inDouble {
		return nil, fmt.Errorf("unmatched quote")
	}
	flush()
	return tokens, nil
}
