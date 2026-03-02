package testsuite

import (
	"bytes"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
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

func assertCLIParityWithPOSIXSh(t *testing.T, bin, script string, compareStderr bool, gopherboxArgs ...string) {
	t.Helper()

	gopherboxRoot := t.TempDir()
	shRoot := t.TempDir()

	args := []string{"--root", gopherboxRoot, "--rw"}
	args = append(args, gopherboxArgs...)
	args = append(args, "-c", script)

	got := runProcess(t, gopherboxRoot, bin, args...)
	want := runPOSIXSh(t, shRoot, script)

	if got.exitCode != want.exitCode {
		t.Fatalf("exit code mismatch: got %d want %d (g.stderr=%q s.stderr=%q)", got.exitCode, want.exitCode, got.stderr, want.stderr)
	}
	if got.stdout != want.stdout {
		t.Fatalf("stdout mismatch: got %q want %q", got.stdout, want.stdout)
	}
	if compareStderr && got.stderr != want.stderr {
		t.Fatalf("stderr mismatch: got %q want %q", got.stderr, want.stderr)
	}
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
			assertCLIParityWithPOSIXSh(t, bin, tc.script, false)
		})
	}
}

func TestCLIInvocationPhaseParityWithPOSIXSh(t *testing.T) {
	t.Parallel()
	bin := buildCLI(t)

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("ok"))
	}))
	defer server.Close()

	cases := []struct {
		name          string
		script        string
		compareStderr bool
		gopherboxArg  []string
	}{
		{
			name: "phase1_file_operations",
			script: strings.Join([]string{
				"mkdir -p docs/sub",
				"echo alpha > docs/sub/a.txt",
				"cp docs/sub/a.txt docs/sub/b.txt",
				"mv docs/sub/b.txt docs/c.txt",
				"ln docs/c.txt docs/link.txt",
				"rm docs/sub/a.txt",
				"cat docs/link.txt",
				"ls docs | sort",
			}, "; "),
		},
		{
			name: "phase1_file_operations_ls_all",
			script: strings.Join([]string{
				"mkdir -p work",
				"cd work",
				"touch .hidden visible",
				"ls -a | sort",
			}, "; "),
		},
		{
			name: "phase1_file_operations_readlink",
			script: strings.Join([]string{
				"mkdir -p links",
				"cd links",
				"touch target.txt",
				"ln -s /target.txt link.txt",
				"readlink link.txt",
			}, "; "),
		},
		{
			name: "phase2_text_processing",
			script: strings.Join([]string{
				"printf 'alpha\\nbeta\\nalpha\\n' > in.txt",
				"grep alpha in.txt | wc -l | awk '{print $1 \":\" $2}'",
				"printf 'b\\na\\na\\n' > sort.txt",
				"sort sort.txt | uniq -c | awk '{print $1 \":\" $2}'",
				"printf 'a,b,c\\n' | cut -d , -f 2 | tr b B | rev",
			}, "; "),
		},
		{
			name:          "phase2_text_processing_compact_n_flags",
			compareStderr: true,
			script: strings.Join([]string{
				"printf '1\\n2\\n3\\n' | head -n2",
				"printf '1\\n2\\n3\\n' | tail -n2",
			}, "; "),
		},
		{
			name:          "phase2_text_processing_grep_prefix_and_errors",
			compareStderr: true,
			script: strings.Join([]string{
				"printf 'alpha\\n' > ok.txt",
				"grep alpha ok.txt missing.txt",
			}, "; "),
		},
		{
			name: "phase2_text_processing_tr_ranges",
			script: strings.Join([]string{
				"printf 'abc\\n' | tr a-z A-Z",
			}, "; "),
		},
		{
			name: "phase2_text_processing_nl_default",
			script: strings.Join([]string{
				"printf '\\nA\\n\\n' | nl",
			}, "; "),
		},
		{
			name: "phase2_text_processing_unexpand_default",
			script: strings.Join([]string{
				"printf 'a        b\\n' | unexpand",
			}, "; "),
		},
		{
			name: "phase2_text_processing_wc_and_uniq_counts",
			script: strings.Join([]string{
				"printf 'a b\\nc\\n' | wc",
				"printf 'a\\na\\nb\\n' | uniq -c",
			}, "; "),
		},
		{
			name: "phase3_data_and_search",
			script: strings.Join([]string{
				"printf '1 2\\n3 4\\n' | awk '{print $1 + $2}'",
				"mkdir -p d/sub",
				"echo x > d/a.txt",
				"echo y > d/sub/b.txt",
				"find d -name '*.txt' | sort",
				"printf 'a b c d' | xargs -n 2 echo item",
				"printf 'hello' | base64 | base64 -d",
				"echo",
			}, "; "),
		},
		{
			name: "phase4_archive_and_network",
			script: strings.Join([]string{
				"mkdir -p arch",
				"echo hello > arch/file.txt",
				"tar -cf out.tar arch",
				"rm -r arch",
				"tar -xf out.tar",
				"cat arch/file.txt",
				"printf 'zipme\\n' > z.txt",
				"gzip z.txt",
				"gunzip z.txt.gz",
				"cat z.txt",
				"curl " + server.URL,
			}, "; "),
			gopherboxArg: []string{"--allow-url-prefix", server.URL},
		},
		{
			name: "phase5_shell_utilities",
			script: strings.Join([]string{
				"env NAME=agent printenv NAME",
				"basename /a/b/c.txt",
				"dirname /a/b/c.txt",
				"seq 3",
				"expr 2 + 3",
				"echo hi | tee out.txt >/dev/null",
				"cat out.txt",
				"true && echo true-ok",
				"false || echo false-ok",
			}, "; "),
		},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			assertCLIParityWithPOSIXSh(t, bin, tc.script, tc.compareStderr, tc.gopherboxArg...)
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
