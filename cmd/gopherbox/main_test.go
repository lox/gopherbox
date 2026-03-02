package main

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRunScriptMode(t *testing.T) {
	t.Parallel()

	stdout := &bytes.Buffer{}
	stderr := &bytes.Buffer{}
	exit := run(context.Background(), []string{"gopherbox", "-c", "echo hi"}, strings.NewReader(""), stdout, stderr)

	if exit != 0 {
		t.Fatalf("exit code mismatch: got %d want 0 (stderr=%q)", exit, stderr.String())
	}
	if got, want := stdout.String(), "hi\n"; got != want {
		t.Fatalf("stdout mismatch: got %q want %q", got, want)
	}
	if got := stderr.String(); got != "" {
		t.Fatalf("unexpected stderr: %q", got)
	}
}

func TestRunScriptCdWithoutArgsUsesHome(t *testing.T) {
	t.Parallel()

	stdout := &bytes.Buffer{}
	stderr := &bytes.Buffer{}
	exit := run(context.Background(), []string{"gopherbox", "-c", "cd; pwd"}, strings.NewReader(""), stdout, stderr)

	if exit != 0 {
		t.Fatalf("exit code mismatch: got %d want 0 (stderr=%q)", exit, stderr.String())
	}
	if got, want := stdout.String(), "/home/user\n"; got != want {
		t.Fatalf("stdout mismatch: got %q want %q", got, want)
	}
}

func TestRunAppletFromGopherboxCommand(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "note.txt"), []byte("hello\n"), 0o644); err != nil {
		t.Fatalf("seed file: %v", err)
	}

	stdout := &bytes.Buffer{}
	stderr := &bytes.Buffer{}
	argv := []string{"gopherbox", "--root", root, "cat", "note.txt"}
	exit := run(context.Background(), argv, strings.NewReader(""), stdout, stderr)

	if exit != 0 {
		t.Fatalf("exit code mismatch: got %d want 0 (stderr=%q)", exit, stderr.String())
	}
	if got, want := stdout.String(), "hello\n"; got != want {
		t.Fatalf("stdout mismatch: got %q want %q", got, want)
	}
	if got := stderr.String(); got != "" {
		t.Fatalf("unexpected stderr: %q", got)
	}
}

func TestRunAppletByProgramName(t *testing.T) {
	t.Parallel()

	stdout := &bytes.Buffer{}
	stderr := &bytes.Buffer{}
	exit := run(context.Background(), []string{"echo", "hello", "world"}, strings.NewReader(""), stdout, stderr)

	if exit != 0 {
		t.Fatalf("exit code mismatch: got %d want 0 (stderr=%q)", exit, stderr.String())
	}
	if got, want := stdout.String(), "hello world\n"; got != want {
		t.Fatalf("stdout mismatch: got %q want %q", got, want)
	}
}

func TestOverlayModeDoesNotWriteThroughByDefault(t *testing.T) {
	t.Parallel()
	root := t.TempDir()

	stdout := &bytes.Buffer{}
	stderr := &bytes.Buffer{}
	argv := []string{"gopherbox", "--root", root, "touch", "overlay.txt"}
	exit := run(context.Background(), argv, strings.NewReader(""), stdout, stderr)

	if exit != 0 {
		t.Fatalf("exit code mismatch: got %d want 0 (stderr=%q)", exit, stderr.String())
	}
	if _, err := os.Stat(filepath.Join(root, "overlay.txt")); !os.IsNotExist(err) {
		t.Fatalf("expected overlay write not to hit disk, stat err=%v", err)
	}
}

func TestReadWriteModeWritesThrough(t *testing.T) {
	t.Parallel()
	root := t.TempDir()

	stdout := &bytes.Buffer{}
	stderr := &bytes.Buffer{}
	argv := []string{"gopherbox", "--root", root, "--rw", "touch", "rw.txt"}
	exit := run(context.Background(), argv, strings.NewReader(""), stdout, stderr)

	if exit != 0 {
		t.Fatalf("exit code mismatch: got %d want 0 (stderr=%q)", exit, stderr.String())
	}
	if _, err := os.Stat(filepath.Join(root, "rw.txt")); err != nil {
		t.Fatalf("expected rw write-through file to exist, err=%v", err)
	}
}

func TestUnknownCommandReturns127(t *testing.T) {
	t.Parallel()

	stdout := &bytes.Buffer{}
	stderr := &bytes.Buffer{}
	exit := run(context.Background(), []string{"gopherbox", "does-not-exist"}, strings.NewReader(""), stdout, stderr)

	if exit != 127 {
		t.Fatalf("exit code mismatch: got %d want 127", exit)
	}
	if got := stderr.String(); !strings.Contains(got, "unknown command") {
		t.Fatalf("expected stderr to mention unknown command, got %q", got)
	}
}

func TestRunShCommandModeScript(t *testing.T) {
	t.Parallel()

	stdout := &bytes.Buffer{}
	stderr := &bytes.Buffer{}
	exit := run(context.Background(), []string{"gopherbox", "sh", "-c", "echo hi"}, strings.NewReader(""), stdout, stderr)

	if exit != 0 {
		t.Fatalf("exit code mismatch: got %d want 0 (stderr=%q)", exit, stderr.String())
	}
	if got, want := stdout.String(), "hi\n"; got != want {
		t.Fatalf("stdout mismatch: got %q want %q", got, want)
	}
}

func TestRunShCommandModeScriptFile(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "script.sh"), []byte("echo from-file\n"), 0o644); err != nil {
		t.Fatalf("seed script: %v", err)
	}

	stdout := &bytes.Buffer{}
	stderr := &bytes.Buffer{}
	argv := []string{"gopherbox", "--root", root, "sh", "script.sh"}
	exit := run(context.Background(), argv, strings.NewReader(""), stdout, stderr)

	if exit != 0 {
		t.Fatalf("exit code mismatch: got %d want 0 (stderr=%q)", exit, stderr.String())
	}
	if got, want := stdout.String(), "from-file\n"; got != want {
		t.Fatalf("stdout mismatch: got %q want %q", got, want)
	}
}
