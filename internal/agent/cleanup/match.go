package cleanup

import (
	"strings"

	"github.com/nebulactrl/nebulactrl/internal/agent/docker"
)

// matcher decides whether an image is in the protected set. The set holds
// image references and local image IDs; an image matches by ID, by a tag it
// carries, or by the digest of one of its repo digests (a dangling image keeps
// its digests, which is how an image-source release survives a tag that moved
// on).
type matcher struct {
	ids     map[string]bool
	tags    map[string]bool
	digests map[string]bool
}

func newMatcher(refs, ids []string) *matcher {
	m := &matcher{ids: map[string]bool{}, tags: map[string]bool{}, digests: map[string]bool{}}
	for _, id := range ids {
		if id = strings.TrimSpace(id); id != "" {
			m.ids[id] = true
		}
	}
	for _, ref := range refs {
		ref = strings.TrimSpace(ref)
		if ref == "" {
			continue
		}
		if strings.HasPrefix(ref, "sha256:") && !strings.Contains(ref, "@") {
			// A bare digest is an image ID (the fallback identity of a release).
			m.ids[ref] = true
			continue
		}
		name, digest, hasDigest := strings.Cut(ref, "@")
		if hasDigest && digest != "" {
			m.digests[digest] = true
		}
		// "repo@sha256:…" names no tag; "repo" alone means repo:latest.
		if name != "" && (!hasDigest || hasTag(name)) {
			m.tags[normTag(name)] = true
		}
	}
	return m
}

// protects reports whether img is in the set.
func (m *matcher) protects(img docker.ImageSummary) bool {
	if m.ids[img.ID] {
		return true
	}
	for _, t := range realTags(img.RepoTags) {
		if m.tags[normTag(t)] {
			return true
		}
	}
	for _, d := range img.RepoDigests {
		if _, digest, ok := strings.Cut(d, "@"); ok && m.digests[digest] {
			return true
		}
	}
	return false
}

// realTags drops the "<none>:<none>" placeholder older daemons list for an
// untagged image.
func realTags(tags []string) []string {
	var out []string
	for _, t := range tags {
		if t != "" && !strings.HasPrefix(t, "<none>") {
			out = append(out, t)
		}
	}
	return out
}

// hasTag reports whether the last path segment of a repository name has a tag.
func hasTag(name string) bool {
	seg := name[strings.LastIndexByte(name, '/')+1:]
	return strings.Contains(seg, ":")
}

// normTag normalizes "repo[:tag]" the way the daemon lists it: the implicit
// Docker Hub host and library/ prefixes dropped and :latest made explicit.
func normTag(name string) string {
	for _, p := range []string{"docker.io/", "index.docker.io/", "registry-1.docker.io/"} {
		name = strings.TrimPrefix(name, p)
	}
	name = strings.TrimPrefix(name, "library/")
	if !hasTag(name) {
		name += ":latest"
	}
	return name
}
