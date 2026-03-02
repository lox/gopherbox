package testsuite

import (
	"context"
	"strings"
	"testing"

	gb "github.com/buildkite/gopherbox"
)

func TestCoreBehaviourSuite(t *testing.T) {
	t.Parallel()

	t.Run("exec_and_exit_status_mapping", func(t *testing.T) {
		t.Parallel()
		shell := gb.New(gb.Config{})

		cases := []struct {
			name   string
			script string
			want   execExpectation
		}{
			{name: "true", script: "true", want: execExpectation{exitCode: 0}},
			{name: "false", script: "false", want: execExpectation{exitCode: 1}},
			{name: "unknown", script: "does-not-exist", want: execExpectation{exitCode: 127, stderrContains: "command not found"}},
		}

		for _, tc := range cases {
			tc := tc
			t.Run(tc.name, func(t *testing.T) {
				res, err := runScript(t, shell, tc.script)
				assertExec(t, res, err, tc.want)
			})
		}
	})

	t.Run("filesystem_persists_across_exec_calls", func(t *testing.T) {
		t.Parallel()
		shell := gb.New(gb.Config{})

		_, err := runScript(t, shell, `echo "hello" > note.txt`)
		if err != nil {
			t.Fatalf("first exec failed: %v", err)
		}

		res, err := runScript(t, shell, `cat note.txt`)
		assertExec(t, res, err, execExpectation{exitCode: 0, stdout: "hello\n"})
	})

	t.Run("cwd_and_env_reset_between_exec_calls", func(t *testing.T) {
		t.Parallel()
		shell := gb.New(gb.Config{})

		res1, err := runScript(t, shell, `cd / && export FOO=bar && pwd`)
		assertExec(t, res1, err, execExpectation{exitCode: 0, stdout: "/\n"})

		res2, err := runScript(t, shell, `pwd; echo ${FOO:-unset}`)
		assertExec(t, res2, err, execExpectation{exitCode: 0})

		lines := strings.Split(strings.TrimSpace(res2.Stdout), "\n")
		if len(lines) != 2 {
			t.Fatalf("unexpected output lines: %q", res2.Stdout)
		}
		if lines[0] != "/home/user" {
			t.Fatalf("cwd reset mismatch: got %q", lines[0])
		}
		if lines[1] != "unset" {
			t.Fatalf("env reset mismatch: got %q", lines[1])
		}
	})

	t.Run("exec_with_overrides", func(t *testing.T) {
		t.Parallel()
		shell := gb.New(gb.Config{})

		res, err := shell.ExecWith(context.Background(), `pwd; echo "$NAME"`, gb.ExecOptions{
			Cwd: "/",
			Env: map[string]string{"NAME": "agent"},
		})
		assertExec(t, res, err, execExpectation{exitCode: 0, stdout: "/\nagent\n"})
	})

	t.Run("custom_command_registration", func(t *testing.T) {
		t.Parallel()
		shell := gb.New(gb.Config{
			CustomCommands: map[string]gb.CommandFunc{
				"hello": func(_ context.Context, _ []string, io gb.CommandIO) int {
					_, _ = io.Stdout.Write([]byte("hi\n"))
					return 0
				},
			},
		})

		res, err := runScript(t, shell, `hello`)
		assertExec(t, res, err, execExpectation{exitCode: 0, stdout: "hi\n"})
	})

	t.Run("cwd_changes_within_single_exec", func(t *testing.T) {
		t.Parallel()
		shell := gb.New(gb.Config{})
		res, err := runScript(t, shell, `pwd; cd /; pwd; cd /home/user; pwd`)
		assertExec(t, res, err, execExpectation{exitCode: 0, stdout: "/home/user\n/\n/home/user\n"})
	})

	t.Run("invalid_override_cwd_returns_error", func(t *testing.T) {
		t.Parallel()
		shell := gb.New(gb.Config{})
		_, err := shell.ExecWith(context.Background(), `pwd`, gb.ExecOptions{Cwd: "/does-not-exist"})
		if err == nil {
			t.Fatalf("expected error for invalid cwd override")
		}
	})
}
