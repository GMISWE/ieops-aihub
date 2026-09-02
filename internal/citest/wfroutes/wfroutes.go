// Package wfroutes extracts the aihub HTTP calls a GitHub Actions workflow
// makes, so a test can assert every one of them hits a route this server
// actually registers.
//
// Why this exists (aihub#181): publish-bins.yml carried a "Notify aihub" step
// that curled `PUT ${AIHUB_URL}/v1/binary` for months. No PUT route has ever
// been registered anywhere in this repo, so every publish 404'd — and the step
// was written as `continue-on-error: true` plus `curl -sf ... || echo "aihub
// notify failed (non-blocking)"`, so the 404 printed one calm line and nothing
// ever went red. Deleting that step fixes the instance; this package is the
// gate that fails the NEXT one at review time instead of never.
//
// The route side is not string-matched out of the Go source. Callers pass the
// routes enumerated from the real echo router (server.NewRouter(...).Routes()),
// so registration-style drift — path groups, a different router, a route moved
// between files — cannot make this gate go quiet.
//
// # The rule
//
// In any workflow step, if a run script expands an aihub base-URL variable and
// a literal path follows it immediately, that (method, path) pair must be a
// registered route. The method comes from -X/--request on the same logical
// command line (backslash continuations joined), defaulting to GET.
//
// # Deliberate limits, stated rather than hidden
//
//   - Scope is .github/workflows only. Shell outside CI (plugins/, scripts/)
//     is not scanned; no such caller exists today, and a text scanner over
//     arbitrary shell would produce false reds that get the gate deleted.
//   - `curl "$AIHUB_URL"` with no path is ignored: an empty suffix is how the
//     `[ -z "$AIHUB_URL" ] && echo skipping` guard lines read, and those are
//     not HTTP calls.
//   - A non-literal path (`${AIHUB_URL}/v1/${kind}`) is NOT skipped — it is
//     reported with Unresolved set, and the test fails on it. Skipping would
//     make "write the path through a variable" the cheapest way to silence
//     this gate, which is the failure mode gates die of.
//
// Detection of the base-URL variable is deliberately doubled along two
// independent dimensions, because one of them alone is cheap to slip past:
// the env KEY name (contains "aihub" and "url"), and the env VALUE expression
// (interpolates a secrets./vars./env. name containing "aihub" and "url").
// Renaming AIHUB_URL to something bland still trips the value detector;
// inlining ${{ secrets.AIHUB_URL }} straight into the run script skips the env
// block entirely and is caught by a third pattern that reads the script.
package wfroutes

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"
)

// Call is one aihub HTTP call found in a workflow.
type Call struct {
	File   string // path as given to the scanner
	Job    string
	Step   string // step name, or "(unnamed)"
	Line   int    // 1-based line in File where the command starts
	Method string // GET/PUT/POST/... ; empty when Unresolved
	Path   string // literal path beginning with "/", e.g. "/v1/binary"
	Raw    string // the logical command line, trimmed
	// Unresolved marks a call whose path could not be read literally. It is
	// still returned, and callers must treat it as a failure — see the package
	// doc on why this is not a skip.
	Unresolved bool
	Reason     string
}

func (c Call) String() string {
	loc := fmt.Sprintf("%s:%d (job %q, step %q)", c.File, c.Line, c.Job, c.Step)
	if c.Unresolved {
		return fmt.Sprintf("%s: unresolved aihub call: %s\n    %s", loc, c.Reason, c.Raw)
	}
	return fmt.Sprintf("%s: %s %s\n    %s", loc, c.Method, c.Path, c.Raw)
}

// Route is a registered server route: the method and the echo-style path,
// where ":name" matches one segment and "*" matches the remainder.
type Route struct {
	Method string
	Path   string
}

var methodVerbs = map[string]bool{
	"GET": true, "HEAD": true, "POST": true, "PUT": true,
	"PATCH": true, "DELETE": true, "OPTIONS": true, "CONNECT": true, "TRACE": true,
}

// ScanDir scans every *.yml / *.yaml file in dir and returns the aihub calls
// found, sorted by file and line.
func ScanDir(dir string) ([]Call, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	var out []Call
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		ext := strings.ToLower(filepath.Ext(e.Name()))
		if ext != ".yml" && ext != ".yaml" {
			continue
		}
		p := filepath.Join(dir, e.Name())
		calls, err := ScanFile(p)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", p, err)
		}
		out = append(out, calls...)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].File != out[j].File {
			return out[i].File < out[j].File
		}
		return out[i].Line < out[j].Line
	})
	return out, nil
}

// ScanFile scans a single workflow file.
func ScanFile(path string) ([]Call, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var doc yaml.Node
	if err := yaml.Unmarshal(b, &doc); err != nil {
		return nil, err
	}
	if len(doc.Content) == 0 {
		return nil, nil
	}
	root := doc.Content[0]

	wfEnv := scalarMap(mapGet(root, "env"))

	var out []Call
	jobs := mapGet(root, "jobs")
	if jobs == nil || jobs.Kind != yaml.MappingNode {
		return nil, nil
	}
	for i := 0; i+1 < len(jobs.Content); i += 2 {
		jobName := jobs.Content[i].Value
		job := jobs.Content[i+1]
		jobEnv := scalarMap(mapGet(job, "env"))

		steps := mapGet(job, "steps")
		if steps == nil || steps.Kind != yaml.SequenceNode {
			continue
		}
		for _, step := range steps.Content {
			run := mapGet(step, "run")
			if run == nil || run.Kind != yaml.ScalarNode {
				continue
			}
			name := "(unnamed)"
			if n := mapGet(step, "name"); n != nil && n.Value != "" {
				name = n.Value
			}
			env := map[string]string{}
			for _, m := range []map[string]string{wfEnv, jobEnv, scalarMap(mapGet(step, "env"))} {
				for k, v := range m {
					env[k] = v
				}
			}
			out = append(out, scanScript(path, jobName, name, run, env)...)
		}
	}
	return out, nil
}

// aihubish reports whether s names the aihub base URL: it must mention both
// "aihub" and "url", in either order, case-insensitively.
func aihubish(s string) bool {
	l := strings.ToLower(s)
	return strings.Contains(l, "aihub") && strings.Contains(l, "url")
}

// interpolation finds ${{ secrets.X }} / ${{ vars.X }} / ${{ env.X }} references.
var interpolation = regexp.MustCompile(`\$\{\{\s*(?:secrets|vars|env)\.([A-Za-z0-9_]+)\s*\}\}`)

// baseVars returns the env keys in this step that hold the aihub base URL.
func baseVars(env map[string]string) []string {
	var vars []string
	for k, v := range env {
		hit := aihubish(k)
		if !hit {
			for _, m := range interpolation.FindAllStringSubmatch(v, -1) {
				if aihubish(m[1]) {
					hit = true
					break
				}
			}
		}
		if hit {
			vars = append(vars, k)
		}
	}
	sort.Strings(vars)
	return vars
}

// pathSuffix captures everything up to the first shell/URL terminator. It
// deliberately allows "$" through so that a non-literal path is reported as
// Unresolved rather than silently truncated into something that looks valid.
const pathSuffix = `([^\s"'` + "`" + `\\;)|&]*)`

var (
	methodFlag = regexp.MustCompile(`(?:^|\s)(?:-X|--request)[=\s]+([A-Za-z]+)`)
	// A ${{ secrets.AIHUB_URL }} written straight into the run script, with no
	// env: block to look at.
	inlineSecret = regexp.MustCompile(`\$\{\{\s*(?:secrets|vars|env)\.([A-Za-z0-9_]+)\s*\}\}` + pathSuffix)
)

func scanScript(file, job, step string, run *yaml.Node, env map[string]string) []Call {
	vars := baseVars(env)

	// Join backslash continuations so a multi-line curl is one logical command,
	// while remembering the physical line each logical line started on.
	type logical struct {
		text string
		line int
	}
	var logicals []logical
	cur := ""
	curLine := 0
	for i, raw := range strings.Split(run.Value, "\n") {
		physical := run.Line + i
		if cur == "" {
			curLine = physical
		}
		trimmed := strings.TrimRight(raw, " \t")
		if strings.HasSuffix(trimmed, `\`) {
			cur += strings.TrimSuffix(trimmed, `\`) + " "
			continue
		}
		cur += trimmed
		logicals = append(logicals, logical{text: cur, line: curLine})
		cur = ""
	}
	if cur != "" {
		logicals = append(logicals, logical{text: cur, line: curLine})
	}

	var patterns []*regexp.Regexp
	for _, v := range vars {
		q := regexp.QuoteMeta(v)
		patterns = append(patterns, regexp.MustCompile(`\$\{`+q+`\}`+pathSuffix))
		patterns = append(patterns, regexp.MustCompile(`\$`+q+`\b`+pathSuffix))
	}

	var out []Call
	for _, lg := range logicals {
		var suffixes []string
		for _, re := range patterns {
			for _, m := range re.FindAllStringSubmatch(lg.text, -1) {
				suffixes = append(suffixes, m[1])
			}
		}
		for _, m := range inlineSecret.FindAllStringSubmatch(lg.text, -1) {
			if aihubish(m[1]) {
				suffixes = append(suffixes, m[2])
			}
		}
		for _, sfx := range suffixes {
			if sfx == "" {
				// A bare expansion with no path: a guard line, not a call.
				continue
			}
			c := Call{
				File: file, Job: job, Step: step, Line: lg.line,
				Raw: strings.TrimSpace(lg.text),
			}
			if !strings.HasPrefix(sfx, "/") || strings.Contains(sfx, "$") {
				c.Unresolved = true
				c.Reason = fmt.Sprintf("path %q is not a literal path; write the endpoint literally so it can be checked against the router", sfx)
				out = append(out, c)
				continue
			}
			c.Path = strings.SplitN(strings.SplitN(sfx, "?", 2)[0], "#", 2)[0]
			c.Method = "GET"
			if m := methodFlag.FindStringSubmatch(lg.text); m != nil {
				c.Method = strings.ToUpper(m[1])
			}
			out = append(out, c)
		}
	}
	return out
}

// Matches reports whether the call hits one of the registered routes.
func Matches(routes []Route, c Call) bool {
	for _, r := range routes {
		if !methodVerbs[strings.ToUpper(r.Method)] {
			continue
		}
		if !strings.EqualFold(r.Method, c.Method) {
			continue
		}
		if pathMatches(r.Path, c.Path) {
			return true
		}
	}
	return false
}

func pathMatches(route, got string) bool {
	route = strings.TrimSuffix(route, "/")
	got = strings.TrimSuffix(got, "/")
	rs := strings.Split(route, "/")
	gs := strings.Split(got, "/")
	for i, seg := range rs {
		if strings.HasPrefix(seg, "*") {
			return true
		}
		if i >= len(gs) {
			return false
		}
		if strings.HasPrefix(seg, ":") {
			if gs[i] == "" {
				return false
			}
			continue
		}
		if seg != gs[i] {
			return false
		}
	}
	return len(rs) == len(gs)
}

func mapGet(n *yaml.Node, key string) *yaml.Node {
	if n == nil || n.Kind != yaml.MappingNode {
		return nil
	}
	for i := 0; i+1 < len(n.Content); i += 2 {
		if n.Content[i].Value == key {
			return n.Content[i+1]
		}
	}
	return nil
}

func scalarMap(n *yaml.Node) map[string]string {
	out := map[string]string{}
	if n == nil || n.Kind != yaml.MappingNode {
		return out
	}
	for i := 0; i+1 < len(n.Content); i += 2 {
		if n.Content[i+1].Kind == yaml.ScalarNode {
			out[n.Content[i].Value] = n.Content[i+1].Value
		}
	}
	return out
}
