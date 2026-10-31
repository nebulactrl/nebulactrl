package main

import (
	"bytes"
	"go/ast"
	"go/parser"
	"go/token"
	"math/rand/v2"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"
	"unicode"

	"github.com/stretchr/testify/require"
)

const (
	integrationDir = "../../tests/integration"
	durationsFile  = integrationDir + "/durations.json"
)

// integrationTests lists the top-level tests of tests/integration from the
// sources (what `go test -list` prints), so this tier-1 test needs no database
// and no Docker.
func integrationTests(t *testing.T) []string {
	t.Helper()
	files, err := filepath.Glob(integrationDir + "/*_test.go")
	require.NoError(t, err)
	require.NotEmpty(t, files)
	var out []string
	fset := token.NewFileSet()
	for _, f := range files {
		file, err := parser.ParseFile(fset, f, nil, 0)
		require.NoError(t, err)
		for _, d := range file.Decls {
			fn, ok := d.(*ast.FuncDecl)
			if !ok || fn.Recv != nil || !strings.HasPrefix(fn.Name.Name, "Test") || fn.Name.Name == "TestMain" {
				continue
			}
			if rest := strings.TrimPrefix(fn.Name.Name, "Test"); rest != "" && unicode.IsLower(rune(rest[0])) {
				continue // TestfooBar is not a test
			}
			out = append(out, fn.Name.Name)
		}
	}
	sort.Strings(out)
	return out
}

func realDurations(t *testing.T) Durations {
	t.Helper()
	d, err := LoadDurations(durationsFile)
	require.NoError(t, err)
	return d
}

// requirePartition fails unless the shards hold every test exactly once.
func requirePartition(t *testing.T, tests []string, shards [][]string) {
	t.Helper()
	count := map[string]int{}
	for _, s := range shards {
		for _, name := range s {
			count[name]++
		}
	}
	for _, name := range tests {
		require.Equalf(t, 1, count[name], "%s must be in exactly one shard", name)
	}
	require.Len(t, count, len(tests), "a shard holds a test that does not exist")
}

func TestFourShardsCoverEveryIntegrationTestExactlyOnce(t *testing.T) {
	tests := integrationTests(t)
	require.Greater(t, len(tests), 40, "the test list was not found")
	d := realDurations(t)

	shards, loads := Assign(tests, d, 4)
	requirePartition(t, tests, shards)
	for i, s := range shards {
		require.NotEmptyf(t, s, "shard %d/4 is empty", i+1)
		t.Logf("shard %d/4: %d tests, ~%.0fs", i+1, len(s), loads[i])
	}

	// The regex of every shard, as tests/run.sh passes it to go test -run, matches
	// its own tests and no other shard's.
	for i := 1; i <= 4; i++ {
		var out bytes.Buffer
		err := run([]string{"-shard", itoa(i) + "/4", "-durations", durationsFile}, strings.NewReader(strings.Join(tests, "\n")+"\nok  \tpkg\t1.0s\n"), &out, &bytes.Buffer{})
		require.NoError(t, err)
		re, err := regexp.Compile(strings.TrimSpace(out.String()))
		require.NoError(t, err)
		for j, s := range shards {
			for _, name := range s {
				require.Equalf(t, i == j+1, re.MatchString(name), "%s in shard %d, regex of shard %d", name, j+1, i)
			}
		}
	}
}

func TestEveryShardCountPartitions(t *testing.T) {
	tests := integrationTests(t)
	d := realDurations(t)
	for n := 1; n <= 12; n++ {
		shards, _ := Assign(tests, d, n)
		require.Len(t, shards, n)
		requirePartition(t, tests, shards)
	}
}

func TestShardsAreBalanced(t *testing.T) {
	tests := integrationTests(t)
	d := realDurations(t)
	_, loads := Assign(tests, d, 4)
	var sum, max, heaviest float64
	for _, l := range loads {
		sum += l
		if l > max {
			max = l
		}
	}
	for _, name := range tests {
		if w := d.Weight(name); w > heaviest {
			heaviest = w
		}
	}
	// Greedy longest-first never ends more than one item above the mean.
	require.LessOrEqual(t, max, sum/4+heaviest, "loads %v", loads)
}

func TestSelectionIsDeterministic(t *testing.T) {
	tests := integrationTests(t)
	d := realDurations(t)
	want, _ := Assign(tests, d, 4)
	shuffled := append([]string(nil), tests...)
	rand.New(rand.NewPCG(1, 2)).Shuffle(len(shuffled), func(i, j int) { shuffled[i], shuffled[j] = shuffled[j], shuffled[i] })
	got, _ := Assign(shuffled, d, 4)
	require.Equal(t, want, got)
}

func TestDurationsFileMatchesTheTests(t *testing.T) {
	tests := integrationTests(t)
	d := realDurations(t)
	known := map[string]bool{}
	for _, name := range tests {
		known[name] = true
		if _, ok := d.Tests[name]; !ok {
			t.Logf("%s is not in durations.json: it weighs the default %.0fs", name, d.DefaultSeconds)
		}
	}
	for name := range d.Tests {
		require.Truef(t, known[name], "durations.json lists %s, which is not a top-level integration test: remove or rename it", name)
	}
}

func TestMissingTestsGetTheDefaultWeight(t *testing.T) {
	d := Durations{
		DefaultSeconds: 50, ParallelFactor: 0.5,
		Tests: map[string]Test{"TestSlow": {Seconds: 100}, "TestPar": {Seconds: 100, Parallel: true}},
	}
	require.Equal(t, 100.0, d.Weight("TestSlow"))
	require.Equal(t, 50.0, d.Weight("TestPar"), "a parallel test adds a fraction of its duration")
	require.Equal(t, 50.0, d.Weight("TestNew"))

	// TestSlow (100) | TestNewA + TestNewB (50 each); the parallel one (50) joins
	// the lower shard when the loads tie.
	shards, loads := Assign([]string{"TestSlow", "TestNewA", "TestNewB", "TestPar"}, d, 2)
	require.Equal(t, [][]string{{"TestPar", "TestSlow"}, {"TestNewA", "TestNewB"}}, shards)
	require.Equal(t, []float64{150, 100}, loads)
}

func TestAssignDropsDuplicatesAndHandlesMoreShardsThanTests(t *testing.T) {
	d := Durations{DefaultSeconds: 10, ParallelFactor: 1}
	shards, _ := Assign([]string{"TestA", "TestA", "TestB"}, d, 3)
	requirePartition(t, []string{"TestA", "TestB"}, shards)
	require.Empty(t, shards[2])

	err := run([]string{"-shard", "3/3", "-durations", durationsFile}, strings.NewReader("TestA\nTestB\n"), &bytes.Buffer{}, &bytes.Buffer{})
	require.ErrorContains(t, err, "has no tests")
}

func TestParseShard(t *testing.T) {
	i, n, err := ParseShard("2/4")
	require.NoError(t, err)
	require.Equal(t, [2]int{2, 4}, [2]int{i, n})
	for _, bad := range []string{"", "2", "0/4", "5/4", "a/4", "1/0", "1/-1", "1/4/4"} {
		_, _, err := ParseShard(bad)
		require.Errorf(t, err, "%q", bad)
	}
}

func TestReadTestsIgnoresGoTestNoise(t *testing.T) {
	got, err := ReadTests(strings.NewReader("TestA\n  TestB  \nok  \tgithub.com/x/y\t0.01s\nSKIP integration: nope\nBenchmarkX\n"))
	require.NoError(t, err)
	require.Equal(t, []string{"TestA", "TestB"}, got)
}

func TestRegexQuotesNames(t *testing.T) {
	require.Equal(t, `^(TestA|TestB)$`, Regex([]string{"TestA", "TestB"}))
	re := regexp.MustCompile(Regex([]string{"TestA"}))
	require.True(t, re.MatchString("TestA"))
	require.False(t, re.MatchString("TestAB"), "-run matches the name anywhere unless anchored")
	require.False(t, re.MatchString("XTestA"))
}

func itoa(i int) string { return string(rune('0' + i)) }

func TestUpdateFromLog(t *testing.T) {
	log := strings.Join([]string{
		"integration: tier full ready after 20s (network slots: 0, 0 = unbounded)",
		"=== RUN   TestSerial",
		"=== RUN   TestPar1",
		"=== PAUSE TestPar1",
		"=== RUN   TestPar2",
		"=== PAUSE TestPar2",
		"=== RUN   TestMulti",
		"--- SKIP: TestMulti (0.00s)",
		"    --- PASS: TestPar1/sub (5.00s)", // a subtest: ignored
		"--- PASS: TestSerial (100.04s)",
		"--- PASS: TestPar1 (60.00s)",
		"--- FAIL: TestPar2 (40.00s)",
		"ok  \tgithub.com/x/tests/integration\t200.500s",
	}, "\n")
	old := Durations{DefaultSeconds: 25, ParallelFactor: 0.9, Tests: map[string]Test{"TestMulti": {Seconds: 900}, "TestGone": {Seconds: 5}}}
	got, err := UpdateFromLog(old, strings.NewReader(log))
	require.NoError(t, err)
	require.Equal(t, map[string]Test{
		"TestSerial": {Seconds: 100},
		"TestPar1":   {Seconds: 60, Parallel: true},
		"TestPar2":   {Seconds: 40, Parallel: true},
		"TestMulti":  {Seconds: 900}, // skipped: keeps its old entry
	}, got.Tests, "tests that are no longer in the log (TestGone) are dropped")
	// 200.5 wall - 20 setup - 100 serial = 80.5 for the group; the parallel tests add up to 100.
	require.InDelta(t, 0.81, got.ParallelFactor, 0.001)
	require.Equal(t, 25.0, got.DefaultSeconds)

	_, err = UpdateFromLog(old, strings.NewReader("nothing\n"))
	require.Error(t, err)

	path := filepath.Join(t.TempDir(), "d.json")
	require.NoError(t, WriteDurations(path, got))
	back, err := LoadDurations(path)
	require.NoError(t, err)
	require.Equal(t, got, back)
}

const goDurationsFile = "../../scripts/ci/go-durations.json"

// goPackages is what the ci workflow feeds the tool: go list ./... of the module.
func goPackages(t *testing.T) []string {
	t.Helper()
	cmd := exec.Command("go", "list", "./...")
	cmd.Dir = "../.."
	out, err := cmd.Output()
	require.NoError(t, err)
	pkgs, err := ReadPackages(bytes.NewReader(out))
	require.NoError(t, err)
	require.Greater(t, len(pkgs), 50, "go list found no packages")
	return pkgs
}

func TestThreeGoShardsCoverEveryPackageExactlyOnce(t *testing.T) {
	pkgs := goPackages(t)
	d, err := LoadDurations(goDurationsFile)
	require.NoError(t, err)

	for n := 1; n <= 5; n++ {
		shards, _ := AssignPreloaded(pkgs, d, n, 90)
		requirePartition(t, pkgs, shards)
	}

	// Through the command line, the way ci.yml calls it: every package is printed by exactly one shard.
	seen := map[string]int{}
	for i := 1; i <= 3; i++ {
		var out bytes.Buffer
		err := run([]string{"-packages", "-shard", itoa(i) + "/3", "-preload-first", "90", "-durations", goDurationsFile},
			strings.NewReader(strings.Join(pkgs, "\n")+"\n"), &out, &bytes.Buffer{})
		require.NoError(t, err)
		got, err := ReadPackages(&out)
		require.NoError(t, err)
		require.NotEmpty(t, got)
		for _, p := range got {
			seen[p]++
		}
	}
	for _, p := range pkgs {
		require.Equalf(t, 1, seen[p], "%s must run in exactly one of the 3 go shards", p)
	}
	require.Len(t, seen, len(pkgs))
}

func TestGoShardsAreBalancedAndVetShardIsPreloaded(t *testing.T) {
	pkgs := goPackages(t)
	d, err := LoadDurations(goDurationsFile)
	require.NoError(t, err)
	shards, loads := AssignPreloaded(pkgs, d, 3, 90)
	var sum, max, heaviest float64
	for _, l := range loads {
		sum += l
		if l > max {
			max = l
		}
	}
	for _, p := range pkgs {
		heaviest = maxf(heaviest, d.Weight(p))
	}
	require.LessOrEqual(t, max, sum/3+heaviest, "loads %v", loads)
	for i := range shards {
		require.NotEmpty(t, shards[i])
		t.Logf("go shard %d/3: %d packages, ~%.0fs (shard 1 includes 90s of go vet)", i+1, len(shards[i]), loads[i])
	}
}

func TestUpdatePackagesFromLogsKeepsTheSlowestRun(t *testing.T) {
	a := "2026-10-28T14:08:17.29Z ok  \tgithub.com/x/app\t235.1s\n2026-10-28T14:08:17.29Z ?   \tgithub.com/x/none\t[no test files]\n"
	b := "ok  \tgithub.com/x/app\t198.7s\nok  \tgithub.com/x/mcp\t30.0s\n"
	old := Durations{DefaultSeconds: 3, ParallelFactor: 1, Tests: map[string]Test{"github.com/x/kept": {Seconds: 9}}}
	got, err := UpdatePackagesFromLogs(old, strings.NewReader(a), strings.NewReader(b))
	require.NoError(t, err)
	require.Equal(t, map[string]Test{
		"github.com/x/app": {Seconds: 235.1}, "github.com/x/mcp": {Seconds: 30}, "github.com/x/kept": {Seconds: 9},
	}, got.Tests)
	_, err = UpdatePackagesFromLogs(old, strings.NewReader("nothing"))
	require.Error(t, err)
}

func maxf(a, b float64) float64 {
	if a > b {
		return a
	}
	return b
}
