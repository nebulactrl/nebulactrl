// Command shardtests picks the integration tests of one CI shard.
//
//	go test -tags integration -list '.*' ./tests/integration/ |
//	  go run ./tools/shardtests -shard 2/4 -durations tests/integration/durations.json
//
// It reads the top-level test names on stdin (lines that do not start with
// "Test" are ignored, so the "ok <pkg>" line of go test -list is harmless) and
// prints a go test -run regex that selects the tests of shard I of N:
//
//	^(TestA|TestB|...)$
//
// The selection is deterministic: tests are sorted by weight, heaviest first,
// and each goes to the shard with the least load so far (greedy longest-first;
// ties go to the lower shard, equal weights are ordered by name). The weights
// come from the durations file, approximate seconds measured from a `-v` run
// (tests/README.md, "Sharding"); a test missing from it gets defaultSeconds.
// -summary prints the estimated load of every shard to stderr.
//
// With -packages the names are Go import paths (go list ./...) and the selected
// ones are printed one per line, for `go test` in the ci workflow:
//
//	go list ./... | go run ./tools/shardtests -packages -shard 1/3 \
//	  -durations scripts/ci/go-durations.json -preload-first 90
//
// -preload-first S counts S seconds of other work (the go vet of shard 1) as
// already on the first shard before the packages are distributed. The package
// durations come from `ok <pkg> 12.3s` lines of CI logs:
//
//	go run ./tools/shardtests -packages -durations scripts/ci/go-durations.json \
//	  -update-from ci-run-1.log,ci-run-2.log
//
// (the slowest value per package wins; every log line may carry a timestamp prefix).
//
// To refresh the durations from a local run:
//
//	./tests/run.sh full -v 2>&1 | tee full.log
//	go run ./tools/shardtests -update-from full.log
//
// rewrites tests/integration/durations.json from the "--- PASS: TestX (12.3s)"
// lines, marks the tests that paused for t.Parallel, and calibrates
// parallelFactor from the wall time of the whole run.
package main

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

// Durations is tests/integration/durations.json.
type Durations struct {
	// Note is free text for the next person who regenerates the file.
	Note string `json:"note,omitempty"`
	// DefaultSeconds is the weight of a test that is not in Tests.
	DefaultSeconds float64 `json:"defaultSeconds"`
	// ParallelFactor scales the weight of parallel tests: they share one control
	// plane and agent and run concurrently, so each adds only a fraction of its
	// own duration to the wall time of a shard.
	ParallelFactor float64 `json:"parallelFactor"`
	// Tests maps a top-level test name to its measured duration.
	Tests map[string]Test `json:"tests"`
}

// Test is the recorded duration of one test.
type Test struct {
	// Seconds is the wall time of the test in a local full run.
	Seconds float64 `json:"seconds"`
	// Parallel marks a test that calls t.Parallel (it runs in the shared group).
	Parallel bool `json:"parallel,omitempty"`
}

// LoadDurations reads and checks a durations file.
func LoadDurations(path string) (Durations, error) {
	var d Durations
	raw, err := os.ReadFile(path)
	if err != nil {
		return d, err
	}
	if err := json.Unmarshal(raw, &d); err != nil {
		return d, fmt.Errorf("%s: %w", path, err)
	}
	if d.DefaultSeconds <= 0 {
		return d, fmt.Errorf("%s: defaultSeconds must be positive", path)
	}
	if d.ParallelFactor <= 0 || d.ParallelFactor > 1 {
		return d, fmt.Errorf("%s: parallelFactor must be in (0, 1]", path)
	}
	for name, t := range d.Tests {
		if t.Seconds < 0 {
			return d, fmt.Errorf("%s: %s has negative seconds", path, name)
		}
	}
	return d, nil
}

// Weight is the load a test adds to its shard.
func (d Durations) Weight(name string) float64 {
	t, ok := d.Tests[name]
	if !ok {
		return d.DefaultSeconds
	}
	if t.Parallel {
		return t.Seconds * d.ParallelFactor
	}
	return t.Seconds
}

// AssignPreloaded distributes tests over n shards and returns the sorted test
// names of every shard (index 0 is shard 1) together with their estimated loads,
// with preload seconds of other work already on the first shard. Every test
// appears in exactly one shard; duplicates in the input are dropped.
func AssignPreloaded(tests []string, d Durations, n int, preload float64) (shards [][]string, loads []float64) {
	seen := make(map[string]bool, len(tests))
	var uniq []string
	for _, t := range tests {
		if !seen[t] {
			seen[t] = true
			uniq = append(uniq, t)
		}
	}
	sort.Slice(uniq, func(i, j int) bool {
		wi, wj := d.Weight(uniq[i]), d.Weight(uniq[j])
		if wi != wj {
			return wi > wj
		}
		return uniq[i] < uniq[j]
	})
	shards = make([][]string, n)
	loads = make([]float64, n)
	loads[0] = preload
	for _, t := range uniq {
		best := 0
		for s := 1; s < n; s++ {
			if loads[s] < loads[best] {
				best = s
			}
		}
		shards[best] = append(shards[best], t)
		loads[best] += d.Weight(t)
	}
	for _, s := range shards {
		sort.Strings(s)
	}
	return shards, loads
}

// Regex builds the go test -run pattern that selects exactly names.
func Regex(names []string) string {
	q := make([]string, len(names))
	for i, n := range names {
		q[i] = regexp.QuoteMeta(n)
	}
	return "^(" + strings.Join(q, "|") + ")$"
}

// ParseShard parses "I/N" (1 <= I <= N).
func ParseShard(s string) (i, n int, err error) {
	a, b, ok := strings.Cut(s, "/")
	if ok {
		i, err = strconv.Atoi(a)
		if err == nil {
			n, err = strconv.Atoi(b)
		}
	}
	if !ok || err != nil || n < 1 || i < 1 || i > n {
		return 0, 0, fmt.Errorf("shard %q: want I/N with 1 <= I <= N, e.g. 1/4", s)
	}
	return i, n, nil
}

// ReadTests returns the lines of r that name a top-level test.
func ReadTests(r io.Reader) ([]string, error) { return readNames(r, "Test") }

// ReadPackages returns the lines of r that look like a Go import path.
func ReadPackages(r io.Reader) ([]string, error) { return readNames(r, "") }

func readNames(r io.Reader, prefix string) ([]string, error) {
	var out []string
	sc := bufio.NewScanner(r)
	for sc.Scan() {
		if f := strings.TrimSpace(sc.Text()); f != "" && strings.HasPrefix(f, prefix) && !strings.ContainsAny(f, " \t") {
			out = append(out, f)
		}
	}
	return out, sc.Err()
}

var rePackage = regexp.MustCompile(`(?:^|\s)ok\s+(\S+)\s+(\d+(?:\.\d+)?)s`)

// UpdatePackagesFromLogs folds the `ok <pkg> 12.3s` lines of go test output (CI
// logs, a timestamp prefix is fine) into d. The slowest value seen per package
// wins, so a contended run does not under-weight a package; the entries of d are
// kept for packages that no log mentions (they may have been cached or filtered).
func UpdatePackagesFromLogs(d Durations, logs ...io.Reader) (Durations, error) {
	tests := map[string]Test{}
	for k, v := range d.Tests {
		tests[k] = v
	}
	fromLogs := map[string]float64{}
	for _, r := range logs {
		sc := bufio.NewScanner(r)
		sc.Buffer(make([]byte, 1<<20), 16<<20)
		for sc.Scan() {
			if m := rePackage.FindStringSubmatch(sc.Text()); m != nil {
				sec, _ := strconv.ParseFloat(m[2], 64)
				fromLogs[m[1]] = max(fromLogs[m[1]], sec)
			}
		}
		if err := sc.Err(); err != nil {
			return d, err
		}
	}
	if len(fromLogs) == 0 {
		return d, errors.New("no \"ok <package> 1.2s\" lines in the logs")
	}
	for pkg, sec := range fromLogs {
		tests[pkg] = Test{Seconds: round1(sec)}
	}
	d.Tests = tests
	if d.Note == "" {
		d.Note = "Approximate seconds of go test per package, the slowest value over the CI logs it was regenerated from; a package that is not listed weighs defaultSeconds (the packages without tests cost about that). Regenerate: go run ./tools/shardtests -packages -update-from <ci logs> (CONTRIBUTING.md, CI is manual)."
	}
	if d.DefaultSeconds <= 0 {
		d.DefaultSeconds = 5
	}
	if d.ParallelFactor <= 0 {
		d.ParallelFactor = 1
	}
	return d, nil
}

var (
	reResult = regexp.MustCompile(`^--- (PASS|FAIL|SKIP): (Test\S+) \((\d+(?:\.\d+)?)s\)`)
	rePause  = regexp.MustCompile(`^=== PAUSE (Test\S+)$`)
	reTotal  = regexp.MustCompile(`^(?:ok|FAIL)\s+\S+\s+(\d+(?:\.\d+)?)s`)
	reReady  = regexp.MustCompile(`integration: tier \S+ ready after (\d+)s`)
)

// UpdateFromLog folds the `go test -v` output in r into d: the top-level test
// durations, the parallel marks, and a recalibrated ParallelFactor. A skipped
// test keeps its previous entry (a test that needs MULTI=1 skips in a normal
// run); a skipped test with no entry is recorded at one second.
func UpdateFromLog(d Durations, r io.Reader) (Durations, error) {
	seconds := map[string]float64{}
	skipped := map[string]bool{}
	parallel := map[string]bool{}
	var total, ready float64
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 1<<20), 16<<20)
	for sc.Scan() {
		line := sc.Text()
		if m := reResult.FindStringSubmatch(line); m != nil {
			sec, _ := strconv.ParseFloat(m[3], 64)
			seconds[m[2]] = sec
			skipped[m[2]] = m[1] == "SKIP"
		} else if m := rePause.FindStringSubmatch(line); m != nil {
			parallel[m[1]] = true
		} else if m := reTotal.FindStringSubmatch(line); m != nil {
			total, _ = strconv.ParseFloat(m[1], 64)
		} else if m := reReady.FindStringSubmatch(line); m != nil {
			ready, _ = strconv.ParseFloat(m[1], 64)
		}
	}
	if err := sc.Err(); err != nil {
		return d, err
	}
	if len(seconds) == 0 {
		return d, errors.New("no top-level \"--- PASS: TestX (1.2s)\" lines in the log (run go test with -v)")
	}
	tests := map[string]Test{}
	for name, sec := range seconds {
		if skipped[name] {
			if old, ok := d.Tests[name]; ok {
				tests[name] = old
			} else {
				tests[name] = Test{Seconds: 1, Parallel: parallel[name]}
			}
			continue
		}
		tests[name] = Test{Seconds: round1(sec), Parallel: parallel[name]}
	}
	d.Tests = tests
	// The wall time of the run is setup + the serial tests + the parallel group,
	// which lasts ParallelFactor of the sum of the parallel tests' own durations.
	var sumSerial, sumParallel, longest float64
	for name, t := range tests {
		if skipped[name] {
			continue
		}
		if t.Parallel {
			sumParallel += t.Seconds
			longest = max(longest, t.Seconds)
		} else {
			sumSerial += t.Seconds
		}
	}
	if total > 0 && sumParallel > 0 {
		group := max(total-ready-sumSerial, longest)
		d.ParallelFactor = min(1, max(0.05, round2(group/sumParallel)))
	}
	if d.Note == "" {
		d.Note = "Approximate wall seconds per top-level integration test from a local ./tests/run.sh full -v run; parallel tests share one control plane, so they count parallelFactor of their duration. Regenerate: go run ./tools/shardtests -update-from full.log (tests/README.md, Sharding)."
	}
	if d.DefaultSeconds <= 0 {
		d.DefaultSeconds = 30
	}
	if d.ParallelFactor <= 0 {
		d.ParallelFactor = 0.3
	}
	return d, nil
}

func round1(f float64) float64 { return float64(int(f*10+0.5)) / 10 }
func round2(f float64) float64 { return float64(int(f*100+0.5)) / 100 }

// marshal is json.Marshal without HTML escaping (the notes mention <angle brackets>).
func marshal(v any) []byte {
	var b bytes.Buffer
	enc := json.NewEncoder(&b)
	enc.SetEscapeHTML(false)
	_ = enc.Encode(v)
	return bytes.TrimSpace(b.Bytes())
}

// WriteDurations writes d with sorted keys and a stable layout (one test per line).
func WriteDurations(path string, d Durations) error {
	names := make([]string, 0, len(d.Tests))
	for n := range d.Tests {
		names = append(names, n)
	}
	sort.Strings(names)
	var b strings.Builder
	note := marshal(d.Note)
	fmt.Fprintf(&b, "{\n  \"note\": %s,\n  \"defaultSeconds\": %v,\n  \"parallelFactor\": %v,\n  \"tests\": {\n", note, d.DefaultSeconds, d.ParallelFactor)
	for i, n := range names {
		t := d.Tests[n]
		raw := marshal(t)
		sep := ","
		if i == len(names)-1 {
			sep = ""
		}
		fmt.Fprintf(&b, "    %q: %s%s\n", n, raw, sep)
	}
	b.WriteString("  }\n}\n")
	return os.WriteFile(path, []byte(b.String()), 0o644)
}

// openLogs opens every path; closeAll closes the ones opened. On error nothing
// stays open.
func openLogs(paths []string) (rs []io.Reader, closeAll func(), err error) {
	var files []*os.File
	closeAll = func() {
		for _, f := range files {
			_ = f.Close()
		}
	}
	for _, p := range paths {
		f, err := os.Open(p)
		if err != nil {
			closeAll()
			return nil, func() {}, err
		}
		files = append(files, f)
		rs = append(rs, f)
	}
	return rs, closeAll, nil
}

func run(args []string, stdin io.Reader, stdout, stderr io.Writer) error {
	fs := flag.NewFlagSet("shardtests", flag.ContinueOnError)
	fs.SetOutput(stderr)
	shard := fs.String("shard", "", "shard to select, I/N (1-based)")
	durations := fs.String("durations", "tests/integration/durations.json", "recorded test durations")
	summary := fs.Bool("summary", false, "print the estimated load of every shard to stderr")
	update := fs.String("update-from", "", "rewrite the durations file from this `go test -v` log (with -packages: comma separated CI logs) and exit")
	packages := fs.Bool("packages", false, "the names are Go import paths (go list ./...); print the selected ones one per line")
	preload := fs.Float64("preload-first", 0, "seconds of other work already on the first shard (with -packages)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *update != "" {
		old, _ := LoadDurations(*durations) // a missing or invalid file starts fresh
		var d Durations
		if *packages {
			rs, closeAll, err := openLogs(strings.Split(*update, ","))
			if err != nil {
				return err
			}
			defer closeAll()
			if d, err = UpdatePackagesFromLogs(old, rs...); err != nil {
				return err
			}
		} else {
			f, err := os.Open(*update)
			if err != nil {
				return err
			}
			defer f.Close()
			if d, err = UpdateFromLog(old, f); err != nil {
				return err
			}
		}
		if err := WriteDurations(*durations, d); err != nil {
			return err
		}
		fmt.Fprintf(stderr, "wrote %s: %d tests, parallelFactor %v\n", *durations, len(d.Tests), d.ParallelFactor)
		return nil
	}
	i, n, err := ParseShard(*shard)
	if err != nil {
		return err
	}
	d, err := LoadDurations(*durations)
	if err != nil {
		return err
	}
	read, what := ReadTests, "tests (expected the output of go test -list)"
	if *packages {
		read, what = ReadPackages, "packages (expected the output of go list ./...)"
	}
	tests, err := read(stdin)
	if err != nil {
		return err
	}
	if len(tests) == 0 {
		return errors.New("no " + what + " on stdin")
	}
	shards, loads := AssignPreloaded(tests, d, n, *preload)
	if *summary {
		for s := range shards {
			fmt.Fprintf(stderr, "shard %d/%d: %d tests, ~%.0fs estimated\n", s+1, n, len(shards[s]), loads[s])
		}
	}
	if len(shards[i-1]) == 0 {
		return fmt.Errorf("shard %d/%d has no tests (%d tests in total)", i, n, len(tests))
	}
	if *packages {
		fmt.Fprintln(stdout, strings.Join(shards[i-1], "\n"))
		return nil
	}
	fmt.Fprintln(stdout, Regex(shards[i-1]))
	return nil
}

func main() {
	if err := run(os.Args[1:], os.Stdin, os.Stdout, os.Stderr); err != nil {
		fmt.Fprintln(os.Stderr, "shardtests:", err)
		os.Exit(2)
	}
}
