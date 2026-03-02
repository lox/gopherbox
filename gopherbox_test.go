package gopherbox

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/spf13/afero"
)

func TestExecBasicCommand(t *testing.T) {
	t.Parallel()
	shell := New(Config{
		Files: map[string]string{
			"/home/user/hello.txt": "world\n",
		},
	})

	res, err := shell.Exec(context.Background(), "cat hello.txt")
	if err != nil {
		t.Fatalf("Exec returned error: %v", err)
	}
	if res.ExitCode != 0 {
		t.Fatalf("unexpected exit code: got %d", res.ExitCode)
	}
	if got, want := res.Stdout, "world\n"; got != want {
		t.Fatalf("stdout mismatch: got %q want %q", got, want)
	}
}

func TestFilesystemPersistsAcrossExecs(t *testing.T) {
	t.Parallel()
	shell := New(Config{})

	if _, err := shell.Exec(context.Background(), `echo "hello" > note.txt`); err != nil {
		t.Fatalf("first exec failed: %v", err)
	}

	res, err := shell.Exec(context.Background(), "cat note.txt")
	if err != nil {
		t.Fatalf("second exec failed: %v", err)
	}
	if got, want := res.Stdout, "hello\n"; got != want {
		t.Fatalf("stdout mismatch: got %q want %q", got, want)
	}
}

func TestEnvAndCwdDoNotPersistAcrossExecs(t *testing.T) {
	t.Parallel()
	shell := New(Config{})

	res1, err := shell.Exec(context.Background(), "cd / && export FOO=bar && pwd")
	if err != nil {
		t.Fatalf("first exec failed: %v", err)
	}
	if got, want := strings.TrimSpace(res1.Stdout), "/"; got != want {
		t.Fatalf("pwd in first exec mismatch: got %q want %q", got, want)
	}

	res2, err := shell.Exec(context.Background(), `pwd; echo ${FOO:-unset}`)
	if err != nil {
		t.Fatalf("second exec failed: %v", err)
	}
	lines := strings.Split(strings.TrimSpace(res2.Stdout), "\n")
	if len(lines) != 2 {
		t.Fatalf("unexpected output lines: %q", res2.Stdout)
	}
	if got, want := lines[0], "/home/user"; got != want {
		t.Fatalf("cwd should reset: got %q want %q", got, want)
	}
	if got, want := lines[1], "unset"; got != want {
		t.Fatalf("env should reset: got %q want %q", got, want)
	}
}

func TestExecWithOverrides(t *testing.T) {
	t.Parallel()
	shell := New(Config{})

	res, err := shell.ExecWith(context.Background(), `pwd; echo "$NAME"`, ExecOptions{
		Cwd: "/",
		Env: map[string]string{"NAME": "agent"},
	})
	if err != nil {
		t.Fatalf("ExecWith failed: %v", err)
	}
	lines := strings.Split(strings.TrimSpace(res.Stdout), "\n")
	if len(lines) != 2 {
		t.Fatalf("unexpected output: %q", res.Stdout)
	}
	if got, want := lines[0], "/"; got != want {
		t.Fatalf("cwd override mismatch: got %q want %q", got, want)
	}
	if got, want := lines[1], "agent"; got != want {
		t.Fatalf("env override mismatch: got %q want %q", got, want)
	}
}

func TestPipesAndRedirects(t *testing.T) {
	t.Parallel()
	shell := New(Config{})

	if _, err := shell.Exec(context.Background(), `printf "a\nb\nc\n" | head -n 2 > out.txt`); err != nil {
		t.Fatalf("pipeline exec failed: %v", err)
	}

	res, err := shell.Exec(context.Background(), "cat out.txt")
	if err != nil {
		t.Fatalf("cat failed: %v", err)
	}
	if got, want := res.Stdout, "a\nb\n"; got != want {
		t.Fatalf("redirect output mismatch: got %q want %q", got, want)
	}
}

func TestCommandCountLimit(t *testing.T) {
	t.Parallel()
	shell := New(Config{
		Files:  map[string]string{"/home/user/a.txt": "x\n"},
		Limits: ExecutionLimits{MaxCommandCount: 2},
	})

	_, err := shell.Exec(context.Background(), "cat a.txt; cat a.txt; cat a.txt")
	if !errors.Is(err, ErrCommandLimitExceeded) {
		t.Fatalf("expected ErrCommandLimitExceeded, got %v", err)
	}
}

func TestOutputLimit(t *testing.T) {
	t.Parallel()
	shell := New(Config{Limits: ExecutionLimits{MaxOutputBytes: 5}})

	_, err := shell.Exec(context.Background(), `printf "123456"`)
	if !errors.Is(err, ErrOutputLimitExceeded) {
		t.Fatalf("expected ErrOutputLimitExceeded, got %v", err)
	}
}

func TestTimeoutLimit(t *testing.T) {
	t.Parallel()
	shell := New(Config{Limits: ExecutionLimits{MaxTimeout: 20 * time.Millisecond}})

	_, err := shell.Exec(context.Background(), "sleep 0.2")
	if !errors.Is(err, ErrTimeoutExceeded) {
		t.Fatalf("expected ErrTimeoutExceeded, got %v", err)
	}
}

func TestOverlayFsDoesNotWriteThrough(t *testing.T) {
	t.Parallel()
	tmp := t.TempDir()
	basePath := filepath.Join(tmp, "file.txt")
	if err := os.WriteFile(basePath, []byte("base\n"), 0o644); err != nil {
		t.Fatalf("failed to seed base file: %v", err)
	}

	shell := New(Config{Fs: OverlayFs(tmp), Cwd: "/"})
	if _, err := shell.Exec(context.Background(), `echo "overlay" > file.txt`); err != nil {
		t.Fatalf("exec failed: %v", err)
	}

	baseBytes, err := os.ReadFile(basePath)
	if err != nil {
		t.Fatalf("failed to read base file: %v", err)
	}
	if got, want := string(baseBytes), "base\n"; got != want {
		t.Fatalf("base file should be unchanged: got %q want %q", got, want)
	}

	res, err := shell.Exec(context.Background(), "cat file.txt")
	if err != nil {
		t.Fatalf("overlay read failed: %v", err)
	}
	if got, want := res.Stdout, "overlay\n"; got != want {
		t.Fatalf("overlay content mismatch: got %q want %q", got, want)
	}
}

func TestReadWriteFsIsJailed(t *testing.T) {
	t.Parallel()
	tmp := t.TempDir()
	shell := New(Config{Fs: ReadWriteFs(tmp), Cwd: "/"})

	if _, err := shell.Exec(context.Background(), `echo "inside" > ../../escape.txt`); err != nil {
		t.Fatalf("exec failed: %v", err)
	}

	insidePath := filepath.Join(tmp, "escape.txt")
	b, err := os.ReadFile(insidePath)
	if err != nil {
		t.Fatalf("failed to read jailed file: %v", err)
	}
	if got, want := string(b), "inside\n"; got != want {
		t.Fatalf("jailed file content mismatch: got %q want %q", got, want)
	}
}

func TestCurlAllowlist(t *testing.T) {
	t.Parallel()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("ok"))
	}))
	defer server.Close()

	shell := New(Config{
		Network: &NetworkConfig{AllowedURLPrefixes: []string{server.URL}},
	})

	res, err := shell.Exec(context.Background(), "curl "+server.URL)
	if err != nil {
		t.Fatalf("curl exec failed: %v", err)
	}
	if res.ExitCode != 0 {
		t.Fatalf("unexpected exit code: %d, stderr=%q", res.ExitCode, res.Stderr)
	}
	if got, want := res.Stdout, "ok"; got != want {
		t.Fatalf("curl output mismatch: got %q want %q", got, want)
	}

	blocked, err := shell.Exec(context.Background(), "curl https://example.com")
	if err != nil {
		t.Fatalf("blocked curl should not return exec error: %v", err)
	}
	if blocked.ExitCode == 0 {
		t.Fatalf("blocked curl should fail")
	}
}

func TestCustomCommandRegistration(t *testing.T) {
	t.Parallel()
	shell := New(Config{
		CustomCommands: map[string]CommandFunc{
			"hello": func(_ context.Context, _ []string, ioCtx CommandIO) int {
				_, _ = ioCtx.Stdout.Write([]byte("hi\n"))
				return 0
			},
		},
	})

	res, err := shell.Exec(context.Background(), "hello")
	if err != nil {
		t.Fatalf("Exec failed: %v", err)
	}
	if got, want := res.Stdout, "hi\n"; got != want {
		t.Fatalf("custom command output mismatch: got %q want %q", got, want)
	}
}

func TestInMemoryFsHelper(t *testing.T) {
	t.Parallel()
	fs := InMemoryFs()
	if err := afero.WriteFile(fs, "/x.txt", []byte("x"), 0o644); err != nil {
		t.Fatalf("write failed: %v", err)
	}
	b, err := afero.ReadFile(fs, "/x.txt")
	if err != nil {
		t.Fatalf("read failed: %v", err)
	}
	if string(b) != "x" {
		t.Fatalf("unexpected read: %q", string(b))
	}
}
