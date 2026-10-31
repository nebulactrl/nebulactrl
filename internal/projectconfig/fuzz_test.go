package projectconfig

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// t2fieldsFingerprint summarises every field of a resolved view in a form that
// compares equal for equal content (NaN included) and never prints addresses.
func t2fieldsFingerprint(r *Resolved) []string {
	if r == nil {
		return nil
	}
	var out []string
	for _, slug := range r.SortedSlugs() {
		rs := r.Services[slug]
		out = append(out, fmt.Sprintf("service %s kind=%s server=%s path=%s", slug, rs.Kind, rs.ServerName, rs.Path))
		for _, id := range rs.FieldIDs() {
			fv := rs.Fields[id]
			out = append(out, fmt.Sprintf("%s|%s|%d:%d|%s|%s", id, fv.Path, fv.Pos.Line, fv.Pos.Column, fv.Applies, Fingerprint(fv.Value)))
		}
	}
	return out
}

// t2exercise runs the whole pipeline on text and checks the invariants that
// hold for every input: no panic, deterministic diagnostics and views, and a
// renderable diagnostic list.
func t2exercise(t testing.TB, data []byte) {
	file, idx, ds := Parse(data, nil)
	_ = ds.RenderText("nebula.toml")
	for _, d := range ds {
		if d.Code == "" || d.Severity == "" || d.Message == "" {
			t.Fatalf("incomplete diagnostic %+v", d)
		}
	}
	file2, _, ds2 := Parse(data, nil)
	if ds.RenderText("") != ds2.RenderText("") {
		t.Fatalf("Parse is not deterministic:\n%s\n---\n%s", ds.RenderText(""), ds2.RenderText(""))
	}
	if file == nil {
		if file2 != nil {
			t.Fatalf("Parse returned a file only the second time")
		}
		return
	}
	envs := append([]string{"", "unknown"}, file.envBlocks()...)
	for _, env := range envs {
		r1, d1 := file.Resolve(env, idx)
		r2, d2 := file.Resolve(env, idx)
		if (r1 == nil) != (r2 == nil) {
			t.Fatalf("env %q: Resolve returned a view only once", env)
		}
		if d1.RenderText("") != d2.RenderText("") {
			t.Fatalf("env %q: Resolve diagnostics are not deterministic", env)
		}
		_ = d1.RenderText("")
		f1, f2 := t2fieldsFingerprint(r1), t2fieldsFingerprint(r2)
		if strings.Join(f1, "\n") != strings.Join(f2, "\n") {
			t.Fatalf("env %q: Resolve is not deterministic:\n%v\n---\n%v", env, f1, f2)
		}
		if r1 != nil {
			for slug, rs := range r1.Services {
				if rs == nil || rs.Slug != slug {
					t.Fatalf("env %q: service %q is malformed", env, slug)
				}
				for id, fv := range rs.Fields {
					if fv.ID != id {
						t.Fatalf("env %q: field %q carries id %q", env, id, fv.ID)
					}
				}
			}
		}
	}
}

func FuzzParseResolveValidate(f *testing.F) {
	seeds := []string{
		"",
		"schema = 1\n",
		"schema = 1\n[services.a]\nimage = \"x\"\n",
		"schema = 1\n[services.a]\nimage = \"x\"\n[environments.e.services.a]\nunset = [\"image\"]\n",
		"schema = 1\n[services.a.git]\nbranch = \"m\"\n[environments.e.services.a]\nunset = ['x.\"y']\n",
		"schema = 1\n[services.a]\nimage = \"x\"\ndepends_on = [\"a\"]\n[services.a.variables]\nX = \"${{a.X}}\"\nY = \"${{\"\n",
		"x = [[[[[[[[[[[[[[[[[[[[\n",
		"[a.b.c.d.e.f.g.h.i.j.k.l.m]\n",
		"\xef\xbb\xbfschema = 1\n",
		"schema = 1\n[services.\"a b\"]\n",
	}
	for _, s := range seeds {
		f.Add([]byte(s))
	}
	for _, dir := range []string{"valid", "invalid"} {
		paths, err := filepath.Glob(filepath.Join("testdata", dir, "*.toml"))
		if err != nil {
			f.Fatal(err)
		}
		for _, path := range paths {
			data, err := os.ReadFile(path)
			if err != nil {
				f.Fatal(err)
			}
			f.Add(data)
		}
	}
	f.Fuzz(func(t *testing.T, data []byte) {
		if len(data) > 1<<16 {
			data = data[:1<<16]
		}
		t2exercise(t, data)
	})
}

// The fuzz seeds run as ordinary tests, so the corpus is checked on every run.
func TestFixturesSurviveTheFuzzBody(t *testing.T) {
	for _, dir := range []string{"valid", "invalid"} {
		for _, path := range fixtureFiles(t, filepath.Join("testdata", dir)) {
			t.Run(filepath.ToSlash(path), func(t *testing.T) {
				data, err := os.ReadFile(path)
				require.NoError(t, err)
				t2exercise(t, data)
			})
		}
	}
}

// t2measure parses and resolves text, returning the wall time and the bytes
// allocated.
func t2measure(text []byte) (time.Duration, uint64) {
	runtime.GC()
	var before, after runtime.MemStats
	runtime.ReadMemStats(&before)
	start := time.Now()
	file, idx, ds := Parse(text, nil)
	if file != nil {
		for _, env := range append([]string{""}, file.envBlocks()...) {
			_, _ = file.Resolve(env, idx)
		}
	}
	_ = ds.RenderText("")
	elapsed := time.Since(start)
	runtime.ReadMemStats(&after)
	return elapsed, after.TotalAlloc - before.TotalAlloc
}

func TestParseTimeBound(t *testing.T) {
	t.Run("a 255 KiB file of many services", func(t *testing.T) {
		var b strings.Builder
		b.WriteString("schema = 1\nserver = \"srv\"\n\n")
		const target = 255 << 10
		services := MaxServices
		per := (target - b.Len()) / services
		for i := range services {
			start := b.Len()
			fmt.Fprintf(&b, "[services.s%02d]\nimage = \"nginx:1\"\nport = 80\n\n[services.s%02d.variables]\n", i, i)
			for n := 0; b.Len()-start < per-48; n++ {
				fmt.Fprintf(&b, "K%04d = \"v%04d-abcdefghijklmnopqrstuvwxyz\"\n", n, n)
			}
			b.WriteString("\n")
		}
		text := []byte(b.String())
		require.Greater(t, len(text), 240<<10)
		require.LessOrEqual(t, len(text), MaxFileBytes)

		start := time.Now()
		file, idx, ds := Parse(text, nil)
		require.NotNil(t, file)
		require.False(t, ds.HasErrors(), ds.RenderText(""))
		r, rds := file.Resolve("", idx)
		require.Empty(t, rds)
		require.Len(t, r.Services, MaxServices)
		require.Less(t, time.Since(start), 5*time.Second, "parsing a maximal file must stay fast")
	})

	t.Run("a maximal file of environment overrides", func(t *testing.T) {
		var b strings.Builder
		b.WriteString("schema = 1\nserver = \"srv\"\n\n[services.web]\nimage = \"nginx:1\"\nport = 80\n\n")
		for i := 0; b.Len() < 240<<10 && i < MaxEnvironments; i++ {
			fmt.Fprintf(&b, "[environments.e%02d.services.web.variables]\n", i)
			for n := 0; n < 150; n++ {
				fmt.Fprintf(&b, "K%04d = \"value-%d\"\n", n, n)
			}
			b.WriteString("\n")
		}
		start := time.Now()
		file, idx, ds := Parse([]byte(b.String()), nil)
		require.NotNil(t, file)
		require.False(t, ds.HasErrors(), ds.RenderText(""))
		for _, env := range file.Environments() {
			_, rds := file.Resolve(env, idx)
			require.Empty(t, rds)
		}
		require.Less(t, time.Since(start), 5*time.Second)
	})

	// The generators of hostile documents. Each is built to the size the
	// caller asks for in bytes.
	nested := map[string]func(n int) string{
		"nested arrays": func(n int) string {
			return "x = " + strings.Repeat("[", n/2) + strings.Repeat("]", n/2) + "\n"
		},
		"unterminated nested arrays": func(n int) string {
			return "x = " + strings.Repeat("[", n) + "\n"
		},
		"nested inline tables": func(n int) string {
			return "x = " + strings.Repeat("{a=", n/4) + "1" + strings.Repeat("}", n/4) + "\n"
		},
		"nested arrays of inline tables": func(n int) string {
			return "x = " + strings.Repeat("[{a=", n/6) + "1" + strings.Repeat("}]", n/6) + "\n"
		},
		"dotted key": func(n int) string {
			return strings.Repeat("a.", n/2) + "a = 1\n"
		},
		"table header": func(n int) string {
			return "[" + strings.Repeat("a.", n/2) + "a]\nx = 1\n"
		},
		"array of tables header": func(n int) string {
			return "[[" + strings.Repeat("a.", n/2) + "a]]\nx = 1\n"
		},
		"service path": func(n int) string {
			const head, tail = "schema = 1\n[services.web.", "a]\nx = 1\n"
			return head + strings.Repeat("a.", max(0, (n-len(head)-len(tail))/2)) + tail
		},
		"many keys": func(n int) string {
			return t2lines(n/12, "k%d = 1\n")
		},
		"many tables": func(n int) string {
			return t2lines(n/10, "[t%d]\n")
		},
		"many array elements": func(n int) string {
			return "x = [" + strings.Repeat("1,", n/2) + "1]\n"
		},
	}
	names := make([]string, 0, len(nested))
	for name := range nested {
		names = append(names, name)
	}
	for _, name := range names {
		gen := nested[name]
		t.Run(name, func(t *testing.T) {
			// A ladder first: work and allocation that grow faster than the
			// input show up on small inputs, before the maximal one is tried.
			for _, n := range []int{512, 2048, 8192} {
				text := []byte(gen(n))
				elapsed, alloc := t2measure(text)
				require.Less(t, elapsed, 2*time.Second, "%d bytes took %s", len(text), elapsed)
				require.Less(t, alloc, uint64(64<<20)+uint64(len(text))*256, "%d bytes allocated %d bytes: more than linear in the input", len(text), alloc)
			}
			text := []byte(gen(MaxFileBytes - 16))
			require.LessOrEqual(t, len(text), MaxFileBytes)
			done := make(chan struct{})
			var elapsed time.Duration
			var alloc uint64
			go func() {
				defer close(done)
				elapsed, alloc = t2measure(text)
			}()
			select {
			case <-done:
			case <-time.After(30 * time.Second):
				t.Fatalf("%d bytes of %s did not finish within 30s", len(text), name)
			}
			require.Less(t, elapsed, 5*time.Second, "%d bytes took %s", len(text), elapsed)
			require.Less(t, alloc, uint64(512<<20), "%d bytes allocated %d bytes", len(text), alloc)
		})
	}

	t.Run("the byte cap itself", func(t *testing.T) {
		text := []byte("schema = 1\n# " + strings.Repeat("x", MaxFileBytes))
		start := time.Now()
		f, _, ds := Parse(text, nil)
		require.Nil(t, f)
		require.True(t, ds.HasCode(CodeFileTooLarge))
		require.Less(t, time.Since(start), time.Second, "an oversized file is refused before it is read")
	})
}
