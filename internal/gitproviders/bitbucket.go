package gitproviders

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
)

// bitbucketDriver receives signed push webhooks from Bitbucket Cloud
// (repo:push) and Bitbucket Data Center (repo:refs_changed). There is no API
// integration: no repository picker, no commit statuses and no previews. The
// user creates the webhook by hand with the URL and secret NebulaCtrl shows.
//
// Cloud: https://support.atlassian.com/bitbucket-cloud/docs/manage-webhooks/
// and https://support.atlassian.com/bitbucket-cloud/docs/event-payloads/
// Data Center: https://confluence.atlassian.com/bitbucketserver/event-payload-938025882.html
type bitbucketDriver struct{ noAPI }

func (bitbucketDriver) provider() Provider { return Bitbucket }

// verify checks X-Hub-Signature ("sha256=<hex>", HMAC-SHA256 of the body).
func (bitbucketDriver) verify(h http.Header, body []byte, secret string) bool {
	sig := h.Get("X-Hub-Signature")
	if !strings.HasPrefix(sig, "sha256=") { // a SHA-1 or unsigned delivery is never accepted
		return false
	}
	return verifyHMAC(secret, body, sig)
}

func (bitbucketDriver) deliveryID(h http.Header) string { return h.Get("X-Request-UUID") }

func isBitbucketCloud(webBase string) bool { return hostOf(webBase) == "bitbucket.org" }

func (bitbucketDriver) cloneURL(webBase, repo string) string {
	if isBitbucketCloud(webBase) {
		return trimBase(webBase) + "/" + repo + ".git"
	}
	return trimBase(webBase) + "/scm/" + strings.ToLower(repo) + ".git" // Data Center
}

func (bitbucketDriver) repoURL(webBase, repo string) string {
	if isBitbucketCloud(webBase) {
		return trimBase(webBase) + "/" + repo
	}
	if proj, slug, ok := strings.Cut(repo, "/"); ok {
		return trimBase(webBase) + "/projects/" + proj + "/repos/" + slug
	}
	return trimBase(webBase) + "/" + repo
}

func (bitbucketDriver) cloneUsername() string         { return "x-token-auth" }
func (bitbucketDriver) apiBase(webBase string) string { return trimBase(webBase) }
func (bitbucketDriver) authHeader(token, _ string) string {
	return "Bearer " + token
}

type bbCloudPush struct {
	Push struct {
		Changes []struct {
			New *struct {
				Type   string `json:"type"`
				Name   string `json:"name"`
				Target struct {
					Hash    string `json:"hash"`
					Message string `json:"message"`
					Author  struct {
						Raw  string `json:"raw"`
						User struct {
							DisplayName string `json:"display_name"`
							Nickname    string `json:"nickname"`
						} `json:"user"`
					} `json:"author"`
				} `json:"target"`
			} `json:"new"`
			Old *struct {
				Target struct {
					Hash string `json:"hash"`
				} `json:"target"`
			} `json:"old"`
		} `json:"changes"`
	} `json:"push"`
	Repository struct {
		FullName string `json:"full_name"`
	} `json:"repository"`
	Actor struct {
		DisplayName string `json:"display_name"`
		Nickname    string `json:"nickname"`
	} `json:"actor"`
}

type bbServerPush struct {
	Actor struct {
		DisplayName string `json:"displayName"`
		Name        string `json:"name"`
	} `json:"actor"`
	Repository struct {
		Slug    string `json:"slug"`
		Project struct {
			Key string `json:"key"`
		} `json:"project"`
	} `json:"repository"`
	Changes []struct {
		Ref struct {
			ID        string `json:"id"`
			DisplayID string `json:"displayId"`
			Type      string `json:"type"`
		} `json:"ref"`
		FromHash string `json:"fromHash"`
		ToHash   string `json:"toHash"`
		Type     string `json:"type"` // ADD | UPDATE | DELETE
	} `json:"changes"`
}

func (bitbucketDriver) parse(h http.Header, body []byte) (parsed, error) {
	ev := h.Get("X-Event-Key")
	p := parsed{event: ev}
	switch ev {
	case "diagnostics:ping":
		p.kind = kindPing
	case "repo:push":
		var in bbCloudPush
		if err := json.Unmarshal(body, &in); err != nil {
			return p, fmt.Errorf("decode push payload: %w", err)
		}
		for _, ch := range in.Push.Changes {
			if ch.New == nil || ch.New.Type != "branch" || ch.New.Target.Hash == "" {
				continue // branch or tag deletion, or a tag push
			}
			author := ch.New.Target.Author.User.DisplayName
			if author == "" {
				author = ch.New.Target.Author.Raw
				if i := strings.Index(author, " <"); i > 0 {
					author = author[:i]
				}
			}
			if author == "" {
				author = in.Actor.DisplayName
			}
			push := PushEvent{
				Provider: Bitbucket, RepoFullName: in.Repository.FullName, Branch: ch.New.Name, CommitSHA: ch.New.Target.Hash,
				CommitMessage: firstLine(ch.New.Target.Message), Author: author,
			}
			if ch.Old != nil {
				push.Before = ch.Old.Target.Hash
			}
			p.pushes = append(p.pushes, push) // PathsComplete stays false: the payload lists no files
		}
		if len(p.pushes) == 0 {
			p.reason = "no branch update in the push"
		} else {
			p.kind = kindPush
		}
	case "repo:refs_changed":
		var in bbServerPush
		if err := json.Unmarshal(body, &in); err != nil {
			return p, fmt.Errorf("decode refs_changed payload: %w", err)
		}
		repo := in.Repository.Project.Key + "/" + in.Repository.Slug
		for _, ch := range in.Changes {
			if ch.Type == "DELETE" || ch.Ref.Type != "BRANCH" || ch.ToHash == "" {
				continue
			}
			p.pushes = append(p.pushes, PushEvent{
				Provider: Bitbucket, RepoFullName: repo, Branch: ch.Ref.DisplayID, CommitSHA: ch.ToHash,
				Before: ch.FromHash, Author: firstNonEmpty(in.Actor.DisplayName, in.Actor.Name),
			})
		}
		if len(p.pushes) == 0 {
			p.reason = "no branch update in the push"
		} else {
			p.kind = kindPush
		}
	default:
		p.reason = "event " + ev + " is not handled (Bitbucket supports push events only)"
	}
	return p, nil
}

func firstNonEmpty(s ...string) string {
	for _, v := range s {
		if v != "" {
			return v
		}
	}
	return ""
}
