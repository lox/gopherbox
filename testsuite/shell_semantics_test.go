package testsuite

import (
	"strings"
	"testing"

	gb "github.com/buildkite/gopherbox"
)

func TestShellSemanticsSuite(t *testing.T) {
	t.Parallel()

	t.Run("multiline_script_with_pipes_and_redirects", func(t *testing.T) {
		t.Parallel()
		shell := gb.New(gb.Config{})

		script := strings.Join([]string{
			"printf 'a\nb\nc\n' > in.txt",
			"cat in.txt | head -n 2 | tail -n 1 > out.txt",
			"cat out.txt",
		}, "\n")

		res, err := runScript(t, shell, script)
		assertExec(t, res, err, execExpectation{exitCode: 0, stdout: "b\n"})
	})

	t.Run("command_substitution_and_variable_expansion", func(t *testing.T) {
		t.Parallel()
		shell := gb.New(gb.Config{})

		res, err := runScript(t, shell, `A=$(printf hello); echo "${A:-missing}-world"`)
		assertExec(t, res, err, execExpectation{exitCode: 0, stdout: "hello-world\n"})
	})

	t.Run("single_and_double_quoting", func(t *testing.T) {
		t.Parallel()
		shell := gb.New(gb.Config{})

		res, err := runScript(t, shell, `X=world; echo 'hello $X'; echo "hello $X"`)
		assertExec(t, res, err, execExpectation{exitCode: 0, stdout: "hello $X\nhello world\n"})
	})

	t.Run("globbing_and_order", func(t *testing.T) {
		t.Parallel()
		shell := gb.New(gb.Config{})
		res, err := runScript(t, shell, `touch c.md a.md b.md; echo *.md`)
		assertExec(t, res, err, execExpectation{exitCode: 0, stdout: "a.md b.md c.md\n"})
	})

	t.Run("logical_short_circuit", func(t *testing.T) {
		t.Parallel()
		shell := gb.New(gb.Config{})
		res, err := runScript(t, shell, `false && echo no; true || echo no; false || echo yes`)
		assertExec(t, res, err, execExpectation{exitCode: 0, stdout: "yes\n"})
	})

	t.Run("heredoc_literal", func(t *testing.T) {
		t.Parallel()
		shell := gb.New(gb.Config{})

		script := `cat <<'EOF'
line 1
line 2
EOF`
		res, err := runScript(t, shell, script)
		assertExec(t, res, err, execExpectation{exitCode: 0, stdout: "line 1\nline 2\n"})
	})

	t.Run("subshell_state_isolation", func(t *testing.T) {
		t.Parallel()
		shell := gb.New(gb.Config{})
		res, err := runScript(t, shell, `(cd /; pwd); pwd`)
		assertExec(t, res, err, execExpectation{exitCode: 0, stdout: "/\n/home/user\n"})
	})

	t.Run("function_definition_and_invocation", func(t *testing.T) {
		t.Parallel()
		shell := gb.New(gb.Config{})

		script := `
say() {
  echo "hello"
}
say
`
		res, err := runScript(t, shell, script)
		assertExec(t, res, err, execExpectation{exitCode: 0, stdout: "hello\n"})
	})

	t.Run("stderr_redirection", func(t *testing.T) {
		t.Parallel()
		shell := gb.New(gb.Config{})

		res, err := runScript(t, shell, `cat missing 2> err.txt; cat err.txt`)
		assertExec(t, res, err, execExpectation{exitCode: 0})
		assertOutputContainsAll(t, res.Stdout, "cat: missing: open /home/user/missing: file does not exist")
	})

	t.Run("append_redirection", func(t *testing.T) {
		t.Parallel()
		shell := gb.New(gb.Config{})
		res, err := runScript(t, shell, `echo one > f.txt; echo two >> f.txt; cat f.txt`)
		assertExec(t, res, err, execExpectation{exitCode: 0, stdout: "one\ntwo\n"})
	})
}
