// Command template-pin resolves and verifies the pinned image digests of the
// template catalog without pulling any image.
//
//	go run ./tools/template-pin templates/catalog            # verify: digest exists, covers amd64+arm64
//	go run ./tools/template-pin -update templates/catalog/x.yaml   # resolve tag → digest and rewrite the file
//
// It shells out to `docker buildx imagetools inspect` so it uses the caller's
// registry credentials (Docker Hub rate limits are per account).
package main

import (
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"

	"github.com/nebulactrl/nebulactrl/internal/templates"
)

type platformSet map[string]bool

type resolved struct {
	digest    string
	platforms platformSet
}

var imageLine = regexp.MustCompile(`^(\s*)(?:-\s+)?image:\s*(\S+)\s*$`)

func main() {
	update := flag.Bool("update", false, "resolve each tag to its current index digest and rewrite the digest lines")
	jobs := flag.Int("j", 4, "parallel registry lookups")
	flag.Parse()
	files, err := expand(flag.Args())
	if err != nil || len(files) == 0 {
		fmt.Fprintln(os.Stderr, "usage: template-pin [-update] [-j N] <file.yaml|dir>...", err)
		os.Exit(2)
	}
	sem := make(chan struct{}, max(*jobs, 1))
	var mu sync.Mutex
	var wg sync.WaitGroup
	failed := false
	report := func(format string, a ...any) {
		mu.Lock()
		defer mu.Unlock()
		failed = true
		fmt.Fprintf(os.Stderr, format+"\n", a...)
	}
	for _, f := range files {
		wg.Add(1)
		sem <- struct{}{}
		go func() {
			defer wg.Done()
			defer func() { <-sem }()
			if err := process(f, *update); err != nil {
				report("%s: %v", f, err)
			} else {
				mu.Lock()
				fmt.Printf("ok  %s\n", filepath.Base(f))
				mu.Unlock()
			}
		}()
	}
	wg.Wait()
	if failed {
		os.Exit(1)
	}
}

func expand(args []string) ([]string, error) {
	var out []string
	for _, a := range args {
		st, err := os.Stat(a)
		if err != nil {
			return nil, err
		}
		if !st.IsDir() {
			out = append(out, a)
			continue
		}
		m, _ := filepath.Glob(filepath.Join(a, "*.yaml"))
		out = append(out, m...)
	}
	sort.Strings(out)
	return out, nil
}

func process(file string, update bool) error {
	raw, err := os.ReadFile(file)
	if err != nil {
		return err
	}
	t, err := templates.Parse(raw)
	if err != nil {
		return err
	}
	want := t.Requires.Arch
	if len(want) == 0 {
		want = []string{"amd64", "arm64"}
	}
	digests := map[string]string{} // image ref → digest to write
	for _, s := range t.Services {
		if s.Image == "" {
			continue
		}
		ref := s.Image
		if !update {
			if s.Digest == "" {
				return fmt.Errorf("service %s: no digest (run with -update)", s.Key)
			}
			repo, _ := templates.SplitImage(s.Image)
			ref = repo + "@" + s.Digest
		}
		r, err := resolve(ref)
		if err != nil {
			return fmt.Errorf("service %s (%s): %w", s.Key, s.Image, err)
		}
		if !update && r.digest != s.Digest {
			return fmt.Errorf("service %s: registry reports digest %s for %s", s.Key, r.digest, ref)
		}
		for _, a := range want {
			if !r.platforms["linux/"+a] {
				return fmt.Errorf("service %s (%s): no linux/%s manifest (has %s); narrow requires.arch or pick another tag", s.Key, s.Image, a, join(r.platforms))
			}
		}
		digests[s.Image] = r.digest
	}
	if !update {
		return nil
	}
	return rewrite(file, raw, digests)
}

// rewrite sets the `digest:` line that follows every `image:` line.
func rewrite(file string, raw []byte, digests map[string]string) error {
	lines := strings.Split(string(raw), "\n")
	var out []string
	for i := 0; i < len(lines); i++ {
		out = append(out, lines[i])
		m := imageLine.FindStringSubmatch(lines[i])
		if m == nil {
			continue
		}
		d, ok := digests[strings.Trim(m[2], `"'`)]
		if !ok {
			continue
		}
		indent := strings.Repeat(" ", len(m[1]))
		if strings.Contains(lines[i], "- image:") {
			indent += "  "
		}
		newLine := indent + "digest: " + d
		if i+1 < len(lines) && strings.HasPrefix(strings.TrimSpace(lines[i+1]), "digest:") {
			i++
		}
		out = append(out, newLine)
	}
	return os.WriteFile(file, []byte(strings.Join(out, "\n")), 0o644)
}

func join(p platformSet) string {
	var s []string
	for k := range p {
		s = append(s, k)
	}
	sort.Strings(s)
	return strings.Join(s, ", ")
}

type manifest struct {
	Digest    string `json:"digest"`
	MediaType string `json:"mediaType"`
	Manifests []struct {
		Platform struct {
			OS           string `json:"os"`
			Architecture string `json:"architecture"`
			Variant      string `json:"variant"`
		} `json:"platform"`
	} `json:"manifests"`
}

func resolve(ref string) (resolved, error) {
	var stdout, stderr bytes.Buffer
	cmd := exec.Command("docker", "buildx", "imagetools", "inspect", "--format", "{{json .Manifest}}", ref)
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := cmd.Run(); err != nil {
		return resolved{}, fmt.Errorf("inspect %s: %s", ref, strings.TrimSpace(stderr.String()))
	}
	var m manifest
	if err := json.Unmarshal(stdout.Bytes(), &m); err != nil {
		return resolved{}, fmt.Errorf("decode manifest of %s: %w", ref, err)
	}
	r := resolved{digest: m.Digest, platforms: platformSet{}}
	if len(m.Manifests) == 0 {
		// Single-platform image: read its platform from the config.
		var out bytes.Buffer
		c2 := exec.Command("docker", "buildx", "imagetools", "inspect", "--format", "{{json .Image}}", ref)
		c2.Stdout = &out
		if err := c2.Run(); err != nil {
			return resolved{}, fmt.Errorf("inspect image config of %s: %w", ref, err)
		}
		var flat struct {
			OS           string `json:"os"`
			Architecture string `json:"architecture"`
		}
		if err := json.Unmarshal(out.Bytes(), &flat); err != nil || flat.OS == "" {
			return resolved{}, fmt.Errorf("cannot determine the platform of %s", ref)
		}
		r.platforms[flat.OS+"/"+flat.Architecture] = true
		return r, nil
	}
	for _, e := range m.Manifests {
		p := e.Platform
		if p.OS == "" || p.OS == "unknown" {
			continue
		}
		r.platforms[p.OS+"/"+p.Architecture] = true
	}
	return r, nil
}
