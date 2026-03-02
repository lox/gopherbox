package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	gb "github.com/buildkite/gopherbox"
	gcmd "github.com/buildkite/gopherbox/commands"
)

type cliOptions struct {
	script           string
	root             string
	cwd              string
	readWrite        bool
	help             bool
	allowURLPrefixes stringSliceFlag
}

type stringSliceFlag []string

func (s *stringSliceFlag) String() string {
	if s == nil {
		return ""
	}
	return strings.Join(*s, ",")
}

func (s *stringSliceFlag) Set(v string) error {
	v = strings.TrimSpace(v)
	if v == "" {
		return fmt.Errorf("value cannot be empty")
	}
	*s = append(*s, v)
	return nil
}

func main() {
	os.Exit(run(context.Background(), os.Args, os.Stdin, os.Stdout, os.Stderr))
}

func run(ctx context.Context, argv []string, stdin io.Reader, stdout, stderr io.Writer) int {
	if len(argv) == 0 {
		argv = []string{"gopherbox"}
	}

	appletName := filepath.Base(argv[0])
	applets := gcmd.DefaultCommands()

	if appletName != "gopherbox" {
		if _, ok := applets[appletName]; ok {
			return runApplet(ctx, defaultOptions(), appletName, argv[1:], stdin, stdout, stderr)
		}
	}

	opts, args, parseExit := parseOptions(appletName, argv[1:], stderr)
	if parseExit != 0 {
		return parseExit
	}

	if opts.help {
		printUsage(stdout, appletName)
		return 0
	}

	if opts.script != "" {
		if len(args) != 0 {
			_, _ = fmt.Fprintln(stderr, "gopherbox: cannot combine -c with command arguments")
			return 2
		}
		return runScript(ctx, opts, opts.script, stdout, stderr)
	}

	if len(args) == 0 {
		printUsage(stderr, appletName)
		return 2
	}

	cmdName := args[0]
	if _, ok := applets[cmdName]; !ok {
		_, _ = fmt.Fprintf(stderr, "gopherbox: unknown command %q\n", cmdName)
		return 127
	}

	return runApplet(ctx, opts, cmdName, args[1:], stdin, stdout, stderr)
}

func defaultOptions() cliOptions {
	return cliOptions{cwd: "/"}
}

func parseOptions(prog string, args []string, stderr io.Writer) (cliOptions, []string, int) {
	opts := defaultOptions()
	fs := flag.NewFlagSet(prog, flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.StringVar(&opts.script, "c", "", "run a shell script")
	fs.StringVar(&opts.root, "root", "", "sandbox root directory (default: current working directory)")
	fs.StringVar(&opts.cwd, "cwd", "/", "working directory inside sandbox")
	fs.BoolVar(&opts.readWrite, "rw", false, "use read-write jailed filesystem (default: copy-on-write overlay)")
	fs.BoolVar(&opts.help, "h", false, "show help")
	fs.BoolVar(&opts.help, "help", false, "show help")
	fs.Var(&opts.allowURLPrefixes, "allow-url-prefix", "allow curl URL prefix (repeatable)")

	if err := fs.Parse(args); err != nil {
		return opts, nil, 2
	}

	return opts, fs.Args(), 0
}

func printUsage(w io.Writer, prog string) {
	_, _ = fmt.Fprintf(w, "Usage: %s [flags] -c <script>\n", prog)
	_, _ = fmt.Fprintf(w, "       %s [flags] <command> [args...]\n", prog)
	_, _ = fmt.Fprintln(w)
	_, _ = fmt.Fprintln(w, "When invoked via symlink as a command name (for example, `echo`),")
	_, _ = fmt.Fprintln(w, "gopherbox runs that built-in command directly (BusyBox-style multicall).")
	_, _ = fmt.Fprintln(w)
	_, _ = fmt.Fprintln(w, "Flags:")
	_, _ = fmt.Fprintln(w, "  -c <script>           Run a shell script")
	_, _ = fmt.Fprintln(w, "  -root <dir>           Sandbox root directory (default: current directory)")
	_, _ = fmt.Fprintln(w, "  -cwd <path>           Working directory inside sandbox (default: /)")
	_, _ = fmt.Fprintln(w, "  -rw                   Use read-write jailed filesystem")
	_, _ = fmt.Fprintln(w, "  -allow-url-prefix P   Allow curl URL prefix (repeatable)")
	_, _ = fmt.Fprintln(w, "  -h, -help             Show help")
}

func runScript(ctx context.Context, opts cliOptions, script string, stdout, stderr io.Writer) int {
	cfg, err := buildConfig(opts)
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "gopherbox: %v\n", err)
		return 1
	}

	shell := gb.New(cfg)
	res, err := shell.Exec(ctx, script)
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "gopherbox: %v\n", err)
		return 1
	}

	_, _ = io.WriteString(stdout, res.Stdout)
	_, _ = io.WriteString(stderr, res.Stderr)
	return clampExitCode(res.ExitCode)
}

func runApplet(ctx context.Context, opts cliOptions, cmdName string, args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	cfg, err := buildConfig(opts)
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "gopherbox: %v\n", err)
		return 1
	}

	if info, statErr := cfg.Fs.Stat(cfg.Cwd); statErr != nil || !info.IsDir() {
		_, _ = fmt.Fprintf(stderr, "gopherbox: cwd does not exist: %s\n", cfg.Cwd)
		return 1
	}

	commands := gcmd.DefaultCommands()
	fn, ok := commands[cmdName]
	if !ok {
		_, _ = fmt.Fprintf(stderr, "gopherbox: unknown command %q\n", cmdName)
		return 127
	}

	env := map[string]string{
		"HOME":     "/home/user",
		"USER":     "user",
		"LOGNAME":  "user",
		"PWD":      cfg.Cwd,
		"PATH":     "/usr/local/bin:/usr/bin:/bin",
		"LANG":     "C.UTF-8",
		"HOSTNAME": "gopherbox",
	}

	var network gcmd.NetworkPolicy
	if cfg.Network != nil {
		network = cfg.Network
	}

	exitCode := fn(ctx, args, gcmd.CommandIO{
		Stdin:   stdin,
		Stdout:  stdout,
		Stderr:  stderr,
		Fs:      cfg.Fs,
		Env:     env,
		Cwd:     cfg.Cwd,
		Network: network,
		Cmds:    commands,
	})

	return clampExitCode(exitCode)
}

func buildConfig(opts cliOptions) (gb.Config, error) {
	root := strings.TrimSpace(opts.root)
	if root == "" {
		wd, err := os.Getwd()
		if err != nil {
			return gb.Config{}, err
		}
		root = wd
	}

	fs := gb.OverlayFs(root)
	if opts.readWrite {
		fs = gb.ReadWriteFs(root)
	}

	cfg := gb.Config{
		Fs:  fs,
		Cwd: opts.cwd,
	}

	if len(opts.allowURLPrefixes) > 0 {
		cfg.Network = &gb.NetworkConfig{
			AllowedURLPrefixes: append([]string(nil), opts.allowURLPrefixes...),
		}
	}

	return cfg, nil
}

func clampExitCode(code int) int {
	if code < 0 {
		return 1
	}
	if code > 255 {
		return 255
	}
	return code
}
