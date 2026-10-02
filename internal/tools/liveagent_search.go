package tools

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

func liveagentGlob(pattern string) (*regexp.Regexp, error) {
	pattern = strings.ReplaceAll(pattern, `\`, "/")
	var b strings.Builder
	b.WriteString("^(?:")
	braces := 0
	for i := 0; i < len(pattern); i++ {
		c := pattern[i]
		switch c {
		case '*':
			if i+1 < len(pattern) && pattern[i+1] == '*' {
				i++
				if i+1 < len(pattern) && pattern[i+1] == '/' {
					i++
					b.WriteString("(?:.*/)?")
				} else {
					b.WriteString(".*")
				}
			} else {
				b.WriteString("[^/]*")
			}
		case '?':
			b.WriteString("[^/]")
		case '{':
			braces++
			b.WriteString("(?:")
		case '}':
			if braces == 0 {
				return nil, errors.New("invalid glob braces")
			}
			braces--
			b.WriteByte(')')
		case ',':
			if braces > 0 {
				b.WriteByte('|')
			} else {
				b.WriteByte(',')
			}
		case '[':
			j := i + 1
			for j < len(pattern) && pattern[j] != ']' {
				j++
			}
			if j == len(pattern) {
				return nil, errors.New("unterminated glob character class")
			}
			cl := pattern[i : j+1]
			if strings.HasPrefix(cl, "[!") {
				cl = "[^" + cl[2:]
			}
			b.WriteString(cl)
			i = j
		default:
			b.WriteString(regexp.QuoteMeta(string(c)))
		}
	}
	if braces != 0 {
		return nil, errors.New("unterminated glob braces")
	}
	b.WriteString(")$")
	return regexp.Compile(b.String())
}

type liveagentIgnore struct {
	base        string
	re          *regexp.Regexp
	negate, dir bool
}

func liveagentIgnoreRules(dir string, git bool) []liveagentIgnore {
	var out []liveagentIgnore
	for _, name := range []string{".gitignore", ".ignore"} {
		if name == ".gitignore" && !git {
			continue
		}
		ignorePath := filepath.Join(dir, name)
		info, statErr := os.Lstat(ignorePath)
		if statErr != nil || !info.Mode().IsRegular() {
			continue
		}
		data, err := os.ReadFile(ignorePath)
		if err != nil {
			continue
		}
		for _, line := range strings.Split(string(data), "\n") {
			line = strings.TrimSpace(line)
			if line == "" || strings.HasPrefix(line, "#") {
				continue
			}
			neg := strings.HasPrefix(line, "!")
			line = strings.TrimPrefix(line, "!")
			directory := strings.HasSuffix(line, "/")
			line = strings.TrimSuffix(line, "/")
			anchored := strings.HasPrefix(line, "/")
			line = strings.TrimPrefix(line, "/")
			if !anchored && !strings.Contains(line, "/") {
				line = "**/" + line
			}
			re, err := liveagentGlob(line)
			if err == nil {
				out = append(out, liveagentIgnore{dir, re, neg, directory})
			}
		}
	}
	return out
}
func liveagentWalk(ctx context.Context, root string, depth int, visit func(string, fs.DirEntry) error) error {
	_, gitErr := os.Stat(filepath.Join(root, ".git"))
	git := gitErr == nil
	var walk func(string, int, []liveagentIgnore) error
	walk = func(dir string, level int, rules []liveagentIgnore) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		entries, err := os.ReadDir(dir)
		if err != nil {
			return err
		}
		rules = append(append([]liveagentIgnore(nil), rules...), liveagentIgnoreRules(dir, git)...)
		for _, d := range entries {
			if err := ctx.Err(); err != nil {
				return err
			}
			path := filepath.Join(dir, d.Name())
			if !d.IsDir() && !d.Type().IsRegular() {
				continue
			}
			if d.Type()&os.ModeSymlink != 0 || d.Name() == ".git" {
				continue
			}
			ignored := false
			for _, rule := range rules {
				rel, _ := filepath.Rel(rule.base, path)
				if rule.re.MatchString(filepath.ToSlash(rel)) && (!rule.dir || d.IsDir()) {
					ignored = !rule.negate
				}
			}
			if ignored {
				continue
			}
			if err := visit(path, d); err != nil {
				return err
			}
			if d.IsDir() && (depth <= 0 || level < depth) {
				if err := walk(path, level+1, rules); err != nil {
					return err
				}
			}
		}
		return nil
	}
	return walk(root, 1, nil)
}
func liveagentSearch(ctx context.Context, name string, a map[string]any) (string, error) {
	raw := laString(a, "path")
	if raw == "" {
		raw = WorkingDir(ctx)
	}
	root, err := liveagentAuthorizePath(ctx, name, raw, "read", true)
	if err != nil {
		return "", err
	}
	info, err := os.Stat(root)
	if err != nil {
		return "", err
	}
	var paths []string
	dirs := map[string]bool{}
	depth := 0
	if name == "List" {
		depth = laInt(a, "depth", 2)
	}
	if !info.IsDir() && name != "Glob" {
		paths = []string{root}
	} else {
		if !info.IsDir() {
			root = filepath.Dir(root)
		}
		err = liveagentWalk(ctx, root, depth, func(p string, d fs.DirEntry) error {
			if _, e := liveagentPath(ctx, p, "read", name, true); e != nil {
				return e
			}
			paths = append(paths, p)
			dirs[p] = d.IsDir()
			return nil
		})
		if err != nil {
			return "", err
		}
	}
	sort.Strings(paths)
	offset := laInt(a, "offset", 0)
	limit := laInt(a, "max_results", 200)
	display := func(p string) string {
		rel, err := filepath.Rel(WorkingDir(ctx), p)
		if err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			return filepath.ToSlash(rel)
		}
		return p
	}
	if name == "List" {
		var rows []string
		for _, p := range paths {
			kind := "FILE"
			if dirs[p] {
				kind = "DIR"
			}
			rows = append(rows, "["+kind+"] "+display(p))
		}
		return liveagentPage(name, root, rows, offset, limit), nil
	}
	pattern := laString(a, "pattern")
	if strings.TrimSpace(pattern) == "" {
		return "", fmt.Errorf("%s.pattern is required", name)
	}
	if name == "Glob" {
		re, err := liveagentGlob(pattern)
		if err != nil {
			return "", err
		}
		var rows []string
		for _, p := range paths {
			rel, _ := filepath.Rel(root, p)
			if !dirs[p] && re.MatchString(filepath.ToSlash(rel)) {
				rows = append(rows, display(p))
			}
		}
		return liveagentPage(name, pattern, rows, offset, limit), nil
	}
	var filters []*regexp.Regexp
	for _, filter := range strings.Split(laString(a, "file_pattern"), "|") {
		if filter != "" {
			re, err := liveagentGlob(filter)
			if err != nil {
				return "", err
			}
			filters = append(filters, re)
		}
	}
	flags := "(?m)"
	if laBool(a, "ignore_case", true) {
		flags += "(?i)"
	}
	if laBool(a, "multiline", false) {
		flags += "(?s)"
	}
	re, err := regexp.Compile(flags + pattern)
	if err != nil {
		return "", err
	}
	var rows []string
	matches, fileCount := 0, 0
	mode := laString(a, "output_mode")
	contextLines := laInt(a, "context", 0)
	for _, p := range paths {
		if err := ctx.Err(); err != nil {
			return "", err
		}
		if dirs[p] {
			continue
		}
		rel, _ := filepath.Rel(root, p)
		if rel == "." {
			rel = filepath.Base(p)
		}
		if len(filters) > 0 {
			ok := false
			for _, f := range filters {
				ok = ok || f.MatchString(filepath.ToSlash(rel))
			}
			if !ok {
				continue
			}
		}
		data, err := os.ReadFile(p)
		if err != nil {
			return "", err
		}
		if IsBinary(data) {
			continue
		}
		text := string(data)
		var indices [][]int
		if laBool(a, "multiline", false) {
			indices = re.FindAllStringIndex(text, -1)
		} else {
			position := 0
			for _, line := range strings.SplitAfter(text, "\n") {
				trimmed := strings.TrimSuffix(line, "\n")
				for _, idx := range re.FindAllStringIndex(trimmed, -1) {
					indices = append(indices, []int{position + idx[0], position + idx[1]})
				}
				position += len(line)
			}
		}
		if len(indices) == 0 {
			continue
		}
		fileCount++
		matches += len(indices)
		if mode == "files" {
			line := 1 + strings.Count(text[:indices[0][0]], "\n")
			rows = append(rows, fmt.Sprintf("%s (%d, firstLine=%d)", display(p), len(indices), line))
			continue
		}
		if mode == "count" {
			continue
		}
		lines := strings.Split(text, "\n")
		for _, index := range indices {
			line := strings.Count(text[:index[0]], "\n")
			last := line + strings.Count(text[index[0]:index[1]], "\n")
			for i := max(0, line-contextLines); i <= min(last+contextLines, len(lines)-1); i++ {
				rows = append(rows, fmt.Sprintf("%s:%d: %s", display(p), i+1, lines[i]))
			}
		}
	}
	if mode == "count" {
		return fmt.Sprintf("Grep: %s\nmatches=%d\nfiles=%d", pattern, matches, fileCount), nil
	}
	return fmt.Sprintf("matches=%d files=%d\n", matches, fileCount) + liveagentPage(name, pattern, rows, offset, laInt(a, "head_limit", 200)), nil
}
func liveagentPage(name, label string, rows []string, offset, limit int) string {
	start := min(offset, len(rows))
	end := min(start+limit, len(rows))
	out := fmt.Sprintf("%s: %s\noffset=%d total=%d hasMore=%t\n", name, label, offset, len(rows), end < len(rows)) + strings.Join(rows[start:end], "\n")
	return Truncate(out)
}
