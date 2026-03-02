package commands

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/spf13/afero"
)

type namedInput struct {
	name string
	data []byte
}

func readNamedInputs(args []string, ioCtx CommandIO) ([]namedInput, int) {
	if len(args) == 0 {
		data, err := io.ReadAll(ioCtx.Stdin)
		if err != nil {
			writeErrf(ioCtx, "read: %v\n", err)
			return nil, 1
		}
		return []namedInput{{name: "-", data: data}}, 0
	}

	inputs := make([]namedInput, 0, len(args))
	exitCode := 0
	for _, arg := range args {
		if arg == "-" {
			data, err := io.ReadAll(ioCtx.Stdin)
			if err != nil {
				writeErrf(ioCtx, "read: %v\n", err)
				exitCode = 1
				continue
			}
			inputs = append(inputs, namedInput{name: "-", data: data})
			continue
		}
		abs := resolvePath(ioCtx.Cwd, arg)
		data, err := afero.ReadFile(ioCtx.Fs, abs)
		if err != nil {
			writeErrf(ioCtx, "%s: %v\n", arg, err)
			exitCode = 1
			continue
		}
		inputs = append(inputs, namedInput{name: arg, data: data})
	}
	return inputs, exitCode
}

func splitLines(data []byte) []string {
	text := strings.ReplaceAll(string(data), "\r\n", "\n")
	text = strings.TrimSuffix(text, "\n")
	if text == "" {
		return nil
	}
	return strings.Split(text, "\n")
}

func cmdGrep(_ context.Context, args []string, ioCtx CommandIO) int {
	ignoreCase := false
	showLineNo := false
	invert := false
	countOnly := false
	fixed := false

	if len(args) == 0 {
		writeErrf(ioCtx, "grep: missing pattern\n")
		return 2
	}

	filtered := make([]string, 0, len(args))
	for _, arg := range args {
		if strings.HasPrefix(arg, "-") && len(filtered) == 0 {
			for _, flag := range arg[1:] {
				switch flag {
				case 'i':
					ignoreCase = true
				case 'n':
					showLineNo = true
				case 'v':
					invert = true
				case 'c':
					countOnly = true
				case 'F':
					fixed = true
				}
			}
			continue
		}
		filtered = append(filtered, arg)
	}

	if len(filtered) == 0 {
		writeErrf(ioCtx, "grep: missing pattern\n")
		return 2
	}

	pattern := filtered[0]
	files := filtered[1:]

	if fixed {
		pattern = regexp.QuoteMeta(pattern)
	}
	if ignoreCase {
		pattern = "(?i)" + pattern
	}

	re, err := regexp.Compile(pattern)
	if err != nil {
		writeErrf(ioCtx, "grep: invalid pattern: %v\n", err)
		return 2
	}

	inputs, readExit := readNamedInputs(files, ioCtx)
	if readExit != 0 && len(inputs) == 0 {
		return 2
	}

	matchedAny := false
	for _, input := range inputs {
		lines := splitLines(input.data)
		matches := 0
		for i, line := range lines {
			isMatch := re.MatchString(line)
			if invert {
				isMatch = !isMatch
			}
			if !isMatch {
				continue
			}
			matchedAny = true
			matches++
			if countOnly {
				continue
			}

			prefix := ""
			if len(inputs) > 1 {
				prefix = input.name + ":"
			}
			if showLineNo {
				prefix += strconv.Itoa(i+1) + ":"
			}
			_, _ = fmt.Fprintf(ioCtx.Stdout, "%s%s\n", prefix, line)
		}
		if countOnly {
			prefix := ""
			if len(inputs) > 1 {
				prefix = input.name + ":"
			}
			_, _ = fmt.Fprintf(ioCtx.Stdout, "%s%d\n", prefix, matches)
		}
	}

	if !matchedAny {
		return 1
	}
	if readExit != 0 {
		return 2
	}
	return 0
}

func cmdEgrep(ctx context.Context, args []string, ioCtx CommandIO) int {
	return cmdGrep(ctx, args, ioCtx)
}

func cmdFgrep(ctx context.Context, args []string, ioCtx CommandIO) int {
	withFixed := append([]string{"-F"}, args...)
	return cmdGrep(ctx, withFixed, ioCtx)
}

func cmdSed(_ context.Context, args []string, ioCtx CommandIO) int {
	if len(args) == 0 {
		writeErrf(ioCtx, "sed: missing script\n")
		return 1
	}

	script := args[0]
	files := args[1:]
	if !strings.HasPrefix(script, "s") {
		writeErrf(ioCtx, "sed: only substitution scripts are supported\n")
		return 1
	}
	if len(script) < 4 {
		writeErrf(ioCtx, "sed: invalid script\n")
		return 1
	}

	delim := script[1]
	parts := strings.Split(script[2:], string(delim))
	if len(parts) < 2 {
		writeErrf(ioCtx, "sed: invalid substitution\n")
		return 1
	}
	pattern := parts[0]
	repl := parts[1]
	flags := ""
	if len(parts) > 2 {
		flags = parts[2]
	}

	re, err := regexp.Compile(pattern)
	if err != nil {
		writeErrf(ioCtx, "sed: %v\n", err)
		return 1
	}

	inputs, readExit := readNamedInputs(files, ioCtx)
	if readExit != 0 && len(inputs) == 0 {
		return 1
	}

	global := strings.Contains(flags, "g")
	for _, in := range inputs {
		lines := splitLines(in.data)
		for _, line := range lines {
			if global {
				line = re.ReplaceAllString(line, repl)
			} else {
				loc := re.FindStringIndex(line)
				if loc != nil {
					line = line[:loc[0]] + re.ReplaceAllString(line[loc[0]:loc[1]], repl) + line[loc[1]:]
				}
			}
			_, _ = fmt.Fprintln(ioCtx.Stdout, line)
		}
	}

	return readExit
}

func cmdHead(_ context.Context, args []string, ioCtx CommandIO) int {
	n := 10
	files := []string{}
	for i := 0; i < len(args); i++ {
		if args[i] == "-n" && i+1 < len(args) {
			val, err := strconv.Atoi(args[i+1])
			if err == nil && val >= 0 {
				n = val
			}
			i++
			continue
		}
		files = append(files, args[i])
	}

	inputs, readExit := readNamedInputs(files, ioCtx)
	for _, in := range inputs {
		lines := splitLines(in.data)
		for i := 0; i < len(lines) && i < n; i++ {
			_, _ = fmt.Fprintln(ioCtx.Stdout, lines[i])
		}
	}
	return readExit
}

func cmdTail(_ context.Context, args []string, ioCtx CommandIO) int {
	n := 10
	files := []string{}
	for i := 0; i < len(args); i++ {
		if args[i] == "-n" && i+1 < len(args) {
			val, err := strconv.Atoi(args[i+1])
			if err == nil && val >= 0 {
				n = val
			}
			i++
			continue
		}
		files = append(files, args[i])
	}

	inputs, readExit := readNamedInputs(files, ioCtx)
	for _, in := range inputs {
		lines := splitLines(in.data)
		start := len(lines) - n
		if start < 0 {
			start = 0
		}
		for _, line := range lines[start:] {
			_, _ = fmt.Fprintln(ioCtx.Stdout, line)
		}
	}
	return readExit
}

func cmdSort(_ context.Context, args []string, ioCtx CommandIO) int {
	reverse := false
	files := []string{}
	for _, arg := range args {
		if arg == "-r" {
			reverse = true
			continue
		}
		files = append(files, arg)
	}

	inputs, readExit := readNamedInputs(files, ioCtx)
	all := []string{}
	for _, in := range inputs {
		all = append(all, splitLines(in.data)...)
	}
	sort.Strings(all)
	if reverse {
		for i := len(all)/2 - 1; i >= 0; i-- {
			op := len(all) - 1 - i
			all[i], all[op] = all[op], all[i]
		}
	}
	for _, line := range all {
		_, _ = fmt.Fprintln(ioCtx.Stdout, line)
	}
	return readExit
}

func cmdUniq(_ context.Context, args []string, ioCtx CommandIO) int {
	showCount := false
	files := []string{}
	for _, arg := range args {
		if arg == "-c" {
			showCount = true
			continue
		}
		files = append(files, arg)
	}

	inputs, readExit := readNamedInputs(files, ioCtx)
	lines := []string{}
	for _, in := range inputs {
		lines = append(lines, splitLines(in.data)...)
	}

	if len(lines) == 0 {
		return readExit
	}

	current := lines[0]
	count := 1
	flush := func() {
		if showCount {
			_, _ = fmt.Fprintf(ioCtx.Stdout, "%7d %s\n", count, current)
		} else {
			_, _ = fmt.Fprintln(ioCtx.Stdout, current)
		}
	}

	for _, line := range lines[1:] {
		if line == current {
			count++
			continue
		}
		flush()
		current = line
		count = 1
	}
	flush()

	return readExit
}

func cmdWc(_ context.Context, args []string, ioCtx CommandIO) int {
	countLines := false
	countWords := false
	countBytes := false
	files := []string{}

	for _, arg := range args {
		if strings.HasPrefix(arg, "-") {
			for _, f := range arg[1:] {
				switch f {
				case 'l':
					countLines = true
				case 'w':
					countWords = true
				case 'c':
					countBytes = true
				}
			}
			continue
		}
		files = append(files, arg)
	}
	if !countLines && !countWords && !countBytes {
		countLines, countWords, countBytes = true, true, true
	}

	inputs, readExit := readNamedInputs(files, ioCtx)
	for _, in := range inputs {
		lines := strings.Count(string(in.data), "\n")
		words := len(strings.Fields(string(in.data)))
		bytesCount := len(in.data)

		parts := []string{}
		if countLines {
			parts = append(parts, fmt.Sprintf("%d", lines))
		}
		if countWords {
			parts = append(parts, fmt.Sprintf("%d", words))
		}
		if countBytes {
			parts = append(parts, fmt.Sprintf("%d", bytesCount))
		}
		parts = append(parts, in.name)
		_, _ = fmt.Fprintln(ioCtx.Stdout, strings.Join(parts, " "))
	}
	return readExit
}

func cmdCut(_ context.Context, args []string, ioCtx CommandIO) int {
	delim := "\t"
	fieldsSpec := ""
	files := []string{}

	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "-d":
			if i+1 < len(args) {
				delim = args[i+1]
				i++
			}
		case "-f":
			if i+1 < len(args) {
				fieldsSpec = args[i+1]
				i++
			}
		default:
			files = append(files, args[i])
		}
	}

	if fieldsSpec == "" {
		writeErrf(ioCtx, "cut: missing -f LIST\n")
		return 1
	}
	fields := parseFieldList(fieldsSpec)

	inputs, readExit := readNamedInputs(files, ioCtx)
	for _, in := range inputs {
		for _, line := range splitLines(in.data) {
			parts := strings.Split(line, delim)
			selected := []string{}
			for _, idx := range fields {
				if idx >= 1 && idx <= len(parts) {
					selected = append(selected, parts[idx-1])
				}
			}
			_, _ = fmt.Fprintln(ioCtx.Stdout, strings.Join(selected, delim))
		}
	}

	return readExit
}

func parseFieldList(spec string) []int {
	indexes := []int{}
	for _, part := range strings.Split(spec, ",") {
		if strings.Contains(part, "-") {
			bounds := strings.SplitN(part, "-", 2)
			start, _ := strconv.Atoi(bounds[0])
			end, _ := strconv.Atoi(bounds[1])
			if start > 0 && end >= start {
				for i := start; i <= end; i++ {
					indexes = append(indexes, i)
				}
			}
			continue
		}
		if idx, err := strconv.Atoi(part); err == nil && idx > 0 {
			indexes = append(indexes, idx)
		}
	}
	return indexes
}

func cmdTr(_ context.Context, args []string, ioCtx CommandIO) int {
	if len(args) != 2 {
		writeErrf(ioCtx, "tr: expected SET1 SET2\n")
		return 1
	}
	from := []rune(args[0])
	to := []rune(args[1])
	mapping := map[rune]rune{}
	for i, r := range from {
		if i < len(to) {
			mapping[r] = to[i]
		} else {
			mapping[r] = to[len(to)-1]
		}
	}

	b, err := io.ReadAll(ioCtx.Stdin)
	if err != nil {
		writeErrf(ioCtx, "tr: %v\n", err)
		return 1
	}
	out := strings.Builder{}
	for _, r := range string(b) {
		if repl, ok := mapping[r]; ok {
			out.WriteRune(repl)
		} else {
			out.WriteRune(r)
		}
	}
	_, _ = io.WriteString(ioCtx.Stdout, out.String())
	return 0
}

func cmdRev(_ context.Context, args []string, ioCtx CommandIO) int {
	inputs, readExit := readNamedInputs(args, ioCtx)
	for _, in := range inputs {
		for _, line := range splitLines(in.data) {
			runes := []rune(line)
			for i, j := 0, len(runes)-1; i < j; i, j = i+1, j-1 {
				runes[i], runes[j] = runes[j], runes[i]
			}
			_, _ = fmt.Fprintln(ioCtx.Stdout, string(runes))
		}
	}
	return readExit
}

func cmdTac(_ context.Context, args []string, ioCtx CommandIO) int {
	inputs, readExit := readNamedInputs(args, ioCtx)
	lines := []string{}
	for _, in := range inputs {
		lines = append(lines, splitLines(in.data)...)
	}
	for i := len(lines) - 1; i >= 0; i-- {
		_, _ = fmt.Fprintln(ioCtx.Stdout, lines[i])
	}
	return readExit
}

func cmdPaste(_ context.Context, args []string, ioCtx CommandIO) int {
	inputs, readExit := readNamedInputs(args, ioCtx)
	if len(inputs) == 0 {
		return readExit
	}
	rows := make([][]string, len(inputs))
	maxRows := 0
	for i, in := range inputs {
		rows[i] = splitLines(in.data)
		if len(rows[i]) > maxRows {
			maxRows = len(rows[i])
		}
	}

	for r := 0; r < maxRows; r++ {
		parts := make([]string, 0, len(rows))
		for _, col := range rows {
			if r < len(col) {
				parts = append(parts, col[r])
			} else {
				parts = append(parts, "")
			}
		}
		_, _ = fmt.Fprintln(ioCtx.Stdout, strings.Join(parts, "\t"))
	}
	return readExit
}

func cmdFold(_ context.Context, args []string, ioCtx CommandIO) int {
	width := 80
	files := []string{}
	for i := 0; i < len(args); i++ {
		if args[i] == "-w" && i+1 < len(args) {
			if n, err := strconv.Atoi(args[i+1]); err == nil && n > 0 {
				width = n
			}
			i++
			continue
		}
		files = append(files, args[i])
	}

	inputs, readExit := readNamedInputs(files, ioCtx)
	for _, in := range inputs {
		for _, line := range splitLines(in.data) {
			runes := []rune(line)
			for len(runes) > width {
				_, _ = fmt.Fprintln(ioCtx.Stdout, string(runes[:width]))
				runes = runes[width:]
			}
			_, _ = fmt.Fprintln(ioCtx.Stdout, string(runes))
		}
	}
	return readExit
}

func cmdNl(_ context.Context, args []string, ioCtx CommandIO) int {
	inputs, readExit := readNamedInputs(args, ioCtx)
	lineNo := 1
	for _, in := range inputs {
		for _, line := range splitLines(in.data) {
			_, _ = fmt.Fprintf(ioCtx.Stdout, "%6d\t%s\n", lineNo, line)
			lineNo++
		}
	}
	return readExit
}

func cmdExpand(_ context.Context, args []string, ioCtx CommandIO) int {
	inputs, readExit := readNamedInputs(args, ioCtx)
	for _, in := range inputs {
		for _, line := range splitLines(in.data) {
			line = strings.ReplaceAll(line, "\t", "        ")
			_, _ = fmt.Fprintln(ioCtx.Stdout, line)
		}
	}
	return readExit
}

func cmdUnexpand(_ context.Context, args []string, ioCtx CommandIO) int {
	inputs, readExit := readNamedInputs(args, ioCtx)
	for _, in := range inputs {
		for _, line := range splitLines(in.data) {
			line = strings.ReplaceAll(line, "        ", "\t")
			_, _ = fmt.Fprintln(ioCtx.Stdout, line)
		}
	}
	return readExit
}

func cmdColumn(_ context.Context, args []string, ioCtx CommandIO) int {
	inputs, readExit := readNamedInputs(args, ioCtx)
	rows := [][]string{}
	colWidths := []int{}

	for _, in := range inputs {
		for _, line := range splitLines(in.data) {
			cols := strings.Fields(line)
			rows = append(rows, cols)
			for i, col := range cols {
				if i >= len(colWidths) {
					colWidths = append(colWidths, utf8.RuneCountInString(col))
					continue
				}
				if w := utf8.RuneCountInString(col); w > colWidths[i] {
					colWidths[i] = w
				}
			}
		}
	}

	for _, row := range rows {
		for i, col := range row {
			pad := colWidths[i] - utf8.RuneCountInString(col)
			if i == len(row)-1 {
				_, _ = fmt.Fprint(ioCtx.Stdout, col)
				continue
			}
			_, _ = fmt.Fprint(ioCtx.Stdout, col)
			_, _ = fmt.Fprint(ioCtx.Stdout, strings.Repeat(" ", pad+2))
		}
		_, _ = fmt.Fprintln(ioCtx.Stdout)
	}

	return readExit
}

func cmdComm(_ context.Context, args []string, ioCtx CommandIO) int {
	if len(args) != 2 {
		writeErrf(ioCtx, "comm: expected FILE1 FILE2\n")
		return 1
	}
	inputs, code := readNamedInputs(args, ioCtx)
	if code != 0 || len(inputs) != 2 {
		return 1
	}

	a := splitLines(inputs[0].data)
	b := splitLines(inputs[1].data)
	setA := map[string]bool{}
	setB := map[string]bool{}
	for _, line := range a {
		setA[line] = true
	}
	for _, line := range b {
		setB[line] = true
	}

	all := map[string]bool{}
	for line := range setA {
		all[line] = true
	}
	for line := range setB {
		all[line] = true
	}
	keys := make([]string, 0, len(all))
	for k := range all {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	for _, line := range keys {
		switch {
		case setA[line] && setB[line]:
			_, _ = fmt.Fprintf(ioCtx.Stdout, "\t\t%s\n", line)
		case setA[line]:
			_, _ = fmt.Fprintln(ioCtx.Stdout, line)
		case setB[line]:
			_, _ = fmt.Fprintf(ioCtx.Stdout, "\t%s\n", line)
		}
	}
	return 0
}

func cmdJoin(_ context.Context, args []string, ioCtx CommandIO) int {
	if len(args) != 2 {
		writeErrf(ioCtx, "join: expected FILE1 FILE2\n")
		return 1
	}
	inputs, code := readNamedInputs(args, ioCtx)
	if code != 0 || len(inputs) != 2 {
		return 1
	}

	left := map[string]string{}
	for _, line := range splitLines(inputs[0].data) {
		parts := strings.Fields(line)
		if len(parts) == 0 {
			continue
		}
		left[parts[0]] = strings.Join(parts[1:], " ")
	}

	for _, line := range splitLines(inputs[1].data) {
		parts := strings.Fields(line)
		if len(parts) == 0 {
			continue
		}
		if other, ok := left[parts[0]]; ok {
			joined := []string{parts[0]}
			if other != "" {
				joined = append(joined, other)
			}
			joined = append(joined, parts[1:]...)
			_, _ = fmt.Fprintln(ioCtx.Stdout, strings.Join(joined, " "))
		}
	}
	return 0
}

func cmdDiff(_ context.Context, args []string, ioCtx CommandIO) int {
	if len(args) != 2 {
		writeErrf(ioCtx, "diff: expected FILE1 FILE2\n")
		return 1
	}
	inputs, code := readNamedInputs(args, ioCtx)
	if code != 0 || len(inputs) != 2 {
		return 1
	}

	a := splitLines(inputs[0].data)
	b := splitLines(inputs[1].data)
	maxLen := len(a)
	if len(b) > maxLen {
		maxLen = len(b)
	}

	different := false
	for i := 0; i < maxLen; i++ {
		var la, lb string
		if i < len(a) {
			la = a[i]
		}
		if i < len(b) {
			lb = b[i]
		}
		if la == lb {
			continue
		}
		different = true
		if la != "" {
			_, _ = fmt.Fprintf(ioCtx.Stdout, "< %s\n", la)
		}
		if lb != "" {
			_, _ = fmt.Fprintf(ioCtx.Stdout, "> %s\n", lb)
		}
	}

	if different {
		return 1
	}
	return 0
}

func cmdStrings(_ context.Context, args []string, ioCtx CommandIO) int {
	inputs, readExit := readNamedInputs(args, ioCtx)
	for _, in := range inputs {
		s := extractStrings(in.data, 4)
		for _, line := range s {
			_, _ = fmt.Fprintln(ioCtx.Stdout, line)
		}
	}
	return readExit
}

func extractStrings(data []byte, minLen int) []string {
	var out []string
	buf := bytes.Buffer{}
	flush := func() {
		if buf.Len() >= minLen {
			out = append(out, buf.String())
		}
		buf.Reset()
	}
	for len(data) > 0 {
		r, size := utf8.DecodeRune(data)
		if r == utf8.RuneError && size == 1 {
			flush()
			data = data[1:]
			continue
		}
		if unicode.IsPrint(r) || r == ' ' || r == '\t' {
			buf.WriteRune(r)
		} else {
			flush()
		}
		data = data[size:]
	}
	flush()
	return out
}
