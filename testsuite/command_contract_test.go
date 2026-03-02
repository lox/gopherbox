package testsuite

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	gb "github.com/buildkite/gopherbox"
)

func TestCommandContractSuite(t *testing.T) {
	t.Parallel()

	t.Run("file_operations", func(t *testing.T) {
		t.Parallel()

		cases := []struct {
			name   string
			script string
			want   execExpectation
		}{
			{
				name: "copy_move_list",
				script: strings.Join([]string{
					"mkdir -p docs",
					"echo alpha > docs/a.txt",
					"cp docs/a.txt docs/b.txt",
					"mv docs/b.txt docs/c.txt",
					"ls docs | sort",
				}, "; "),
				want: execExpectation{exitCode: 0, stdout: "a.txt\nc.txt\n"},
			},
			{
				name: "touch_stat_file",
				script: strings.Join([]string{
					"touch note.txt",
					"echo note >> note.txt",
					"stat note.txt",
					"file note.txt",
				}, "; "),
				want: execExpectation{exitCode: 0},
			},
			{
				name: "hard_link_emulation",
				script: strings.Join([]string{
					"echo hi > src.txt",
					"ln src.txt dst.txt",
					"rm src.txt",
					"cat dst.txt",
				}, "; "),
				want: execExpectation{exitCode: 0, stdout: "hi\n"},
			},
		}

		for _, tc := range cases {
			tc := tc
			t.Run(tc.name, func(t *testing.T) {
				shell := gb.New(gb.Config{})
				res, err := runScript(t, shell, tc.script)
				assertExec(t, res, err, tc.want)
				if tc.name == "touch_stat_file" {
					assertOutputContainsAll(t, res.Stdout, "File: note.txt", "note.txt: text")
				}
			})
		}
	})

	t.Run("text_processing", func(t *testing.T) {
		t.Parallel()

		cases := []struct {
			name   string
			script string
			want   execExpectation
		}{
			{
				name: "grep_sed_chain",
				script: strings.Join([]string{
					"printf 'alpha\nbeta\nalphabet\n' > in.txt",
					"grep alpha in.txt | sed 's/alpha/ALPHA/g'",
				}, "; "),
				want: execExpectation{exitCode: 0, stdout: "ALPHA\nALPHAbet\n"},
			},
			{
				name: "sort_uniq_wc",
				script: strings.Join([]string{
					"printf 'b\na\na\n' > in.txt",
					"sort in.txt | uniq -c",
				}, "; "),
				want: execExpectation{exitCode: 0, stdout: "   2 a\n   1 b\n"},
			},
			{
				name: "cut_tr_rev",
				script: strings.Join([]string{
					"printf 'a,b,c\n' > in.txt",
					"cut -d , -f 2 in.txt | tr b B | rev",
				}, "; "),
				want: execExpectation{exitCode: 0, stdout: "B\n"},
			},
		}

		for _, tc := range cases {
			tc := tc
			t.Run(tc.name, func(t *testing.T) {
				shell := gb.New(gb.Config{})
				res, err := runScript(t, shell, tc.script)
				assertExec(t, res, err, tc.want)
			})
		}
	})

	t.Run("data_and_search", func(t *testing.T) {
		t.Parallel()

		cases := []struct {
			name   string
			script string
			want   execExpectation
		}{
			{
				name: "awk_find_xargs",
				script: strings.Join([]string{
					"printf '1 2\n3 4\n' | awk '{print $1 + $2}'",
					"mkdir -p d && echo x > d/a.txt && find d -name '*.txt'",
					"printf 'a b c' | xargs -n 2 echo item",
				}, "; "),
				want: execExpectation{exitCode: 0},
			},
			{
				name: "jq_base64_hash",
				script: strings.Join([]string{
					"echo '{\"x\":1,\"y\":2}' > obj.json",
					"cat obj.json | jq '.x + .y'",
					"printf 'hello' | base64 | base64 -d",
					"printf 'hello' > h.txt",
					"sha256sum h.txt",
				}, "; "),
				want: execExpectation{exitCode: 0},
			},
		}

		for _, tc := range cases {
			tc := tc
			t.Run(tc.name, func(t *testing.T) {
				shell := gb.New(gb.Config{})
				res, err := runScript(t, shell, tc.script)
				assertExec(t, res, err, tc.want)
				if tc.name == "awk_find_xargs" {
					assertOutputContainsAll(t, res.Stdout, "3\n7\n", "d/a.txt", "item a b", "item c")
				}
				if tc.name == "jq_base64_hash" {
					assertOutputContainsAll(t, res.Stdout, "3\n", "hello", "2cf24dba5fb0a30e")
				}
			})
		}
	})

	t.Run("archive_and_network", func(t *testing.T) {
		t.Parallel()
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			_, _ = w.Write([]byte("ok"))
		}))
		defer server.Close()

		shell := gb.New(gb.Config{
			Network: &gb.NetworkConfig{AllowedURLPrefixes: []string{server.URL}},
		})

		script := strings.Join([]string{
			"mkdir -p arch",
			"echo hello > arch/file.txt",
			"tar -cf out.tar arch",
			"rm -r arch",
			"tar -xf out.tar",
			"cat arch/file.txt",
			"echo zipme > z.txt",
			"gzip z.txt",
			"zcat z.txt.gz",
			"gunzip z.txt.gz",
			"cat z.txt",
			"curl " + server.URL,
		}, "; ")

		res, err := runScript(t, shell, script)
		assertExec(t, res, err, execExpectation{exitCode: 0})
		assertOutputContainsAll(t, res.Stdout, "hello\n", "zipme\n", "ok")
	})

	t.Run("misc_utilities", func(t *testing.T) {
		t.Parallel()
		shell := gb.New(gb.Config{})

		script := strings.Join([]string{
			"env NAME=agent printenv NAME",
			"basename /a/b/c.txt",
			"dirname /a/b/c.txt",
			"seq 3",
			"expr 2 + 3",
			"which cat",
			"whoami",
			"hostname",
			"echo hi | tee out.txt >/dev/null",
			"cat out.txt",
			"timeout 0.01 sleep 0.1 || echo timeout-hit",
			"env time true",
		}, "; ")

		res, err := runScript(t, shell, script)
		assertExec(t, res, err, execExpectation{exitCode: 0, stderrContains: "real "})
		assertOutputContainsAll(t, res.Stdout, "agent\n", "c.txt\n", "/a/b\n", "1\n2\n3\n", "5\n", "/bin/cat\n", "user\n", "gopherbox\n", "hi\n", "timeout-hit\n")
	})
}
