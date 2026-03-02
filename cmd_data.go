package gopherbox

import (
	"bytes"
	"context"
	"crypto/md5"
	"crypto/sha1"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"hash"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	awkinterp "github.com/benhoyt/goawk/interp"
	awkparser "github.com/benhoyt/goawk/parser"
	"github.com/itchyny/gojq"
	"github.com/spf13/afero"
)

func cmdAwk(_ context.Context, args []string, ioCtx CommandIO) int {
	if len(args) == 0 {
		writeErrf(ioCtx, "awk: missing program\n")
		return 1
	}

	script := args[0]
	files := args[1:]
	prog, err := awkparser.ParseProgram([]byte(script), nil)
	if err != nil {
		writeErrf(ioCtx, "awk: %v\n", err)
		return 1
	}

	var input io.Reader
	if len(files) == 0 {
		input = ioCtx.Stdin
	} else {
		buf := bytes.Buffer{}
		for i, f := range files {
			if f == "-" {
				_, _ = io.Copy(&buf, ioCtx.Stdin)
			} else {
				b, err := afero.ReadFile(ioCtx.Fs, resolvePath(ioCtx.Cwd, f))
				if err != nil {
					writeErrf(ioCtx, "awk: %s: %v\n", f, err)
					return 1
				}
				buf.Write(b)
			}
			if i < len(files)-1 {
				buf.WriteByte('\n')
			}
		}
		input = &buf
	}

	_, err = awkinterp.ExecProgram(prog, &awkinterp.Config{
		Stdin:  input,
		Output: ioCtx.Stdout,
		Error:  ioCtx.Stderr,
	})
	if err != nil {
		writeErrf(ioCtx, "awk: %v\n", err)
		return 1
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
	err := afero.Walk(ioCtx.Fs, startAbs, func(path string, info os.FileInfo, err error) error {
		if err != nil {
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
		writeErrf(ioCtx, "find: %v\n", err)
		return 1
	}
	return 0
}

func cmdXargs(ctx context.Context, args []string, ioCtx CommandIO) int {
	maxArgs := 0
	filtered := []string{}
	for i := 0; i < len(args); i++ {
		if args[i] == "-n" && i+1 < len(args) {
			if n, err := strconv.Atoi(args[i+1]); err == nil && n > 0 {
				maxArgs = n
			}
			i++
			continue
		}
		filtered = append(filtered, args[i])
	}
	if len(filtered) == 0 {
		filtered = []string{"echo"}
	}

	data, err := io.ReadAll(ioCtx.Stdin)
	if err != nil {
		writeErrf(ioCtx, "xargs: %v\n", err)
		return 1
	}
	tokens := strings.Fields(string(data))
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
		return 1
	}
	query, err := gojq.Parse(args[0])
	if err != nil {
		writeErrf(ioCtx, "jq: %v\n", err)
		return 1
	}

	inputs, readExit := readNamedInputs(args[1:], ioCtx)
	if len(inputs) == 0 {
		inputs = []namedInput{{name: "-", data: mustReadAll(ioCtx.Stdin)}}
	}

	encoder := json.NewEncoder(ioCtx.Stdout)
	encoder.SetEscapeHTML(false)

	exitCode := readExit
	for _, input := range inputs {
		dec := json.NewDecoder(bytes.NewReader(input.data))
		for {
			var value any
			if err := dec.Decode(&value); err != nil {
				if err == io.EOF {
					break
				}
				writeErrf(ioCtx, "jq: %s: %v\n", input.name, err)
				exitCode = 1
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
					exitCode = 1
					continue
				}
				if err := encoder.Encode(v); err != nil {
					writeErrf(ioCtx, "jq: %v\n", err)
					exitCode = 1
				}
			}
		}
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
	return hashCommand(args, ioCtx, md5.New)
}

func cmdSHA1Sum(_ context.Context, args []string, ioCtx CommandIO) int {
	return hashCommand(args, ioCtx, sha1.New)
}

func cmdSHA256Sum(_ context.Context, args []string, ioCtx CommandIO) int {
	return hashCommand(args, ioCtx, sha256.New)
}

func hashCommand(args []string, ioCtx CommandIO, newHash func() hash.Hash) int {
	if len(args) == 0 {
		h := newHash()
		if _, err := io.Copy(h, ioCtx.Stdin); err != nil {
			writeErrf(ioCtx, "hash: %v\n", err)
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
			writeErrf(ioCtx, "%s: %v\n", arg, err)
			exitCode = 1
			continue
		}
		h := newHash()
		if _, err := io.Copy(h, f); err != nil {
			writeErrf(ioCtx, "%s: %v\n", arg, err)
			exitCode = 1
		}
		_ = f.Close()
		_, _ = fmt.Fprintf(ioCtx.Stdout, "%s  %s\n", hex.EncodeToString(h.Sum(nil)), arg)
	}
	return exitCode
}
