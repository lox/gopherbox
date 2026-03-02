package testsuite

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/spf13/afero"

	gb "github.com/buildkite/gopherbox"
)

func TestSecurityBoundarySuite(t *testing.T) {
	t.Parallel()

	t.Run("readwrite_fs_jails_traversal", func(t *testing.T) {
		t.Parallel()
		root := t.TempDir()
		shell := gb.New(gb.Config{Fs: gb.ReadWriteFs(root), Cwd: "/"})

		res, err := runScript(t, shell, `echo inside > ../../escape.txt; cat /escape.txt`)
		assertExec(t, res, err, execExpectation{exitCode: 0, stdout: "inside\n"})

		b, readErr := os.ReadFile(filepath.Join(root, "escape.txt"))
		if readErr != nil {
			t.Fatalf("failed to read jailed file: %v", readErr)
		}
		if string(b) != "inside\n" {
			t.Fatalf("unexpected jailed file content: %q", string(b))
		}
	})

	t.Run("overlay_fs_does_not_write_through", func(t *testing.T) {
		t.Parallel()
		baseDir := t.TempDir()
		baseFile := filepath.Join(baseDir, "data.txt")
		if err := os.WriteFile(baseFile, []byte("base\n"), 0o644); err != nil {
			t.Fatalf("seed base file: %v", err)
		}

		shell := gb.New(gb.Config{Fs: gb.OverlayFs(baseDir), Cwd: "/"})
		res, err := runScript(t, shell, `echo overlay > data.txt; cat data.txt`)
		assertExec(t, res, err, execExpectation{exitCode: 0, stdout: "overlay\n"})

		b, readErr := os.ReadFile(baseFile)
		if readErr != nil {
			t.Fatalf("read base file: %v", readErr)
		}
		if string(b) != "base\n" {
			t.Fatalf("base file modified unexpectedly: %q", string(b))
		}
	})

	t.Run("readonly_fs_blocks_mutation", func(t *testing.T) {
		t.Parallel()
		mem := gb.InMemoryFs()
		if err := afero.WriteFile(mem, "/x.txt", []byte("x"), 0o644); err != nil {
			t.Fatalf("seed readonly fs: %v", err)
		}

		shell := gb.New(gb.Config{Fs: afero.NewReadOnlyFs(mem), Cwd: "/"})
		res, err := runScript(t, shell, `touch y.txt`)
		assertExec(t, res, err, execExpectation{exitCode: 1})
	})

	t.Run("network_disabled_by_default", func(t *testing.T) {
		t.Parallel()
		shell := gb.New(gb.Config{})
		res, err := runScript(t, shell, `curl https://example.com`)
		assertExec(t, res, err, execExpectation{exitCode: 1, stderrContains: "network is disabled"})
	})

	t.Run("network_allowlist_and_method_policy", func(t *testing.T) {
		t.Parallel()
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			_, _ = w.Write([]byte("ok"))
		}))
		defer server.Close()

		shell := gb.New(gb.Config{Network: &gb.NetworkConfig{AllowedURLPrefixes: []string{server.URL}}})

		resAllow, errAllow := runScript(t, shell, `curl `+server.URL)
		assertExec(t, resAllow, errAllow, execExpectation{exitCode: 0, stdout: "ok"})

		resMethod, errMethod := runScript(t, shell, `curl -X POST `+server.URL)
		assertExec(t, resMethod, errMethod, execExpectation{exitCode: 1, stderrContains: "method not allowed"})

		resURL, errURL := runScript(t, shell, `curl https://not-allowed.example/path`)
		assertExec(t, resURL, errURL, execExpectation{exitCode: 1, stderrContains: "URL not allowed"})
	})

	t.Run("symlink_edges_are_explicit", func(t *testing.T) {
		t.Parallel()
		shell := gb.New(gb.Config{})

		res, err := runScript(t, shell, `echo x > src.txt; ln -s src.txt dst.txt`)
		assertExec(t, res, err, execExpectation{exitCode: 1, stderrContains: "symbolic links not supported"})
	})

	t.Run("readwrite_fs_relative_symlink_resolves_from_cwd", func(t *testing.T) {
		t.Parallel()
		root := t.TempDir()
		shell := gb.New(gb.Config{Fs: gb.ReadWriteFs(root), Cwd: "/"})

		res, err := runScript(t, shell, `mkdir -p d; echo hi > d/a; cd d; ln -s a b; cat b`)
		assertExec(t, res, err, execExpectation{exitCode: 0, stdout: "hi\n"})
	})

	t.Run("readwrite_fs_errors_do_not_leak_host_paths", func(t *testing.T) {
		t.Parallel()
		root := t.TempDir()
		shell := gb.New(gb.Config{Fs: gb.ReadWriteFs(root), Cwd: "/"})

		res, err := runScript(t, shell, `cat missing; cp missing dst; mkdir -p d/sub; rmdir d`)
		assertExec(t, res, err, execExpectation{exitCode: 1})
		assertOutputContainsAll(t, res.Stderr, "cat: missing:", "cp: cannot stat 'missing':", "rmdir: d:")
		if strings.Contains(res.Stderr, root) {
			t.Fatalf("stderr leaked host path %q: %q", root, res.Stderr)
		}
	})

	t.Run("output_timeout_and_command_limits", func(t *testing.T) {
		t.Parallel()

		outShell := gb.New(gb.Config{Limits: gb.ExecutionLimits{MaxOutputBytes: 8}})
		_, outErr := runScript(t, outShell, `printf "0123456789"`)
		if !errors.Is(outErr, gb.ErrOutputLimitExceeded) {
			t.Fatalf("expected ErrOutputLimitExceeded, got %v", outErr)
		}

		timeShell := gb.New(gb.Config{Limits: gb.ExecutionLimits{MaxTimeout: 25 * time.Millisecond}})
		_, timeoutErr := runScript(t, timeShell, `sleep 1`)
		if !errors.Is(timeoutErr, gb.ErrTimeoutExceeded) {
			t.Fatalf("expected ErrTimeoutExceeded, got %v", timeoutErr)
		}

		countShell := gb.New(gb.Config{Limits: gb.ExecutionLimits{MaxCommandCount: 2}})
		_, countErr := runScript(t, countShell, `cat /dev/null; cat /dev/null; cat /dev/null`)
		if !errors.Is(countErr, gb.ErrCommandLimitExceeded) {
			t.Fatalf("expected ErrCommandLimitExceeded, got %v", countErr)
		}
	})

	t.Run("loop_and_call_depth_limits_enforced", func(t *testing.T) {
		t.Parallel()

		loopShell := gb.New(gb.Config{Limits: gb.ExecutionLimits{MaxLoopIter: 5}})
		_, loopErr := runScript(t, loopShell, `while true; do :; done`)
		if !errors.Is(loopErr, gb.ErrLoopLimitExceeded) {
			t.Fatalf("expected ErrLoopLimitExceeded, got %v", loopErr)
		}

		callShell := gb.New(gb.Config{Limits: gb.ExecutionLimits{MaxCallDepth: 3}})
		_, callErr := runScript(t, callShell, `f(){ f; }; f`)
		if !errors.Is(callErr, gb.ErrCallDepthExceeded) {
			t.Fatalf("expected ErrCallDepthExceeded, got %v", callErr)
		}
	})
}
