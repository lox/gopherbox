package testsuite

import (
	"bytes"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

type cliRunResult struct {
	exitCode int
	stdout   string
	stderr   string
}

func buildCLI(t *testing.T) string {
	t.Helper()
	bin := filepath.Join(t.TempDir(), "gopherbox")
	cmd := exec.Command("go", "build", "-o", bin, "../cmd/gopherbox")
	cmd.Dir = "."
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("build cli failed: %v\n%s", err, string(output))
	}
	return bin
}

func runProcess(t *testing.T, cwd, bin string, args ...string) cliRunResult {
	t.Helper()
	cmd := exec.Command(bin, args...)
	cmd.Dir = cwd

	stdout := &bytes.Buffer{}
	stderr := &bytes.Buffer{}
	cmd.Stdout = stdout
	cmd.Stderr = stderr

	err := cmd.Run()
	if err == nil {
		return cliRunResult{exitCode: 0, stdout: stdout.String(), stderr: stderr.String()}
	}

	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		return cliRunResult{exitCode: exitErr.ExitCode(), stdout: stdout.String(), stderr: stderr.String()}
	}

	t.Fatalf("run process failed: %v", err)
	return cliRunResult{}
}

func runPOSIXSh(t *testing.T, cwd, script string) cliRunResult {
	t.Helper()
	return runProcess(t, cwd, "/bin/sh", "-c", script)
}

func TestCLIInvocationShellCommandMode(t *testing.T) {
	t.Parallel()
	bin := buildCLI(t)
	cwd := t.TempDir()

	res := runProcess(t, cwd, bin, "sh", "-c", "echo hello")
	if res.exitCode != 0 {
		t.Fatalf("exit code mismatch: got %d want 0 (stderr=%q)", res.exitCode, res.stderr)
	}
	if got, want := res.stdout, "hello\n"; got != want {
		t.Fatalf("stdout mismatch: got %q want %q", got, want)
	}
}

func TestCLIInvocationParitySubsetWithPOSIXSh(t *testing.T) {
	t.Parallel()
	bin := buildCLI(t)

	cases := []struct {
		name   string
		script string
	}{
		{name: "function_exit_status", script: `f(){ false; }; f; echo $?`},
		{name: "logical_short_circuit", script: `false && echo no; true || echo no; false || echo yes`},
		{name: "globbing_order", script: `touch c.md a.md b.md; echo *.md`},
		{name: "heredoc_literal", script: "cat <<'EOF'\nline 1\nline 2\nEOF"},
		{name: "pipe_and_redirection", script: `printf 'a\nb\nc\n' > in.txt; cat in.txt | head -n 2 | tail -n 1`},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			cwd := t.TempDir()
			got := runProcess(t, cwd, bin, "-c", tc.script)
			want := runPOSIXSh(t, cwd, tc.script)

			if got.exitCode != want.exitCode {
				t.Fatalf("exit code mismatch: got %d want %d (g.stderr=%q s.stderr=%q)", got.exitCode, want.exitCode, got.stderr, want.stderr)
			}
			if got.stdout != want.stdout {
				t.Fatalf("stdout mismatch: got %q want %q", got.stdout, want.stdout)
			}
		})
	}
}

func TestCLIInvocationShScriptFile(t *testing.T) {
	t.Parallel()
	bin := buildCLI(t)
	cwd := t.TempDir()

	scriptPath := filepath.Join(cwd, "script.sh")
	if err := os.WriteFile(scriptPath, []byte("echo from-file\n"), 0o644); err != nil {
		t.Fatalf("seed script file: %v", err)
	}

	got := runProcess(t, cwd, bin, "sh", "script.sh")
	if got.exitCode != 0 {
		t.Fatalf("exit code mismatch: got %d want 0 (stderr=%q)", got.exitCode, got.stderr)
	}
	if got.stdout != "from-file\n" {
		t.Fatalf("stdout mismatch: got %q want %q", got.stdout, "from-file\n")
	}
}
