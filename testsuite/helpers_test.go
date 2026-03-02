package testsuite

import (
	"context"
	"errors"
	"strings"
	"testing"

	gb "github.com/buildkite/gopherbox"
)

type execExpectation struct {
	execErr        error
	exitCode       int
	stdout         string
	stderrContains string
}

func runScript(t *testing.T, shell *gb.Shell, script string) (*gb.Result, error) {
	t.Helper()
	return shell.Exec(context.Background(), script)
}

func assertExec(t *testing.T, res *gb.Result, err error, want execExpectation) {
	t.Helper()

	if want.execErr != nil {
		if !errors.Is(err, want.execErr) {
			t.Fatalf("expected exec error %v, got %v", want.execErr, err)
		}
		return
	}

	if err != nil {
		t.Fatalf("unexpected exec error: %v", err)
	}
	if res == nil {
		t.Fatalf("expected non-nil result")
	}
	if res.ExitCode != want.exitCode {
		t.Fatalf("exit code mismatch: got %d want %d (stderr=%q)", res.ExitCode, want.exitCode, res.Stderr)
	}
	if want.stdout != "" && res.Stdout != want.stdout {
		t.Fatalf("stdout mismatch: got %q want %q", res.Stdout, want.stdout)
	}
	if want.stderrContains != "" && !strings.Contains(res.Stderr, want.stderrContains) {
		t.Fatalf("stderr mismatch: got %q to contain %q", res.Stderr, want.stderrContains)
	}
}

func assertOutputContainsAll(t *testing.T, output string, lines ...string) {
	t.Helper()
	for _, line := range lines {
		if !strings.Contains(output, line) {
			t.Fatalf("expected output to contain %q, got %q", line, output)
		}
	}
}
