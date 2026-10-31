package deployhooks

import (
	"encoding/json"
	"net/http"
	"strings"
)

// Sources a call can come from.
const (
	SourceGeneric   = "generic"   // curl, CI: no payload the hook understands
	SourceDockerHub = "dockerhub" // Docker Hub repository webhook
	SourceGHCR      = "ghcr"      // GitHub `package` / `registry_package` webhook
)

// registryPush is a push parsed from a registry payload.
type registryPush struct {
	Source string
	// Image is the registry-qualified repository, e.g. "docker.io/acme/api".
	Image imageRef
	Tag   string
	// Ignore, when set, is why the delivery cannot deploy anything (a ping, an
	// untagged manifest, a package type other than a container image).
	Ignore string
}

// dockerHubPayload is the part of Docker Hub's webhook body the hook reads.
// https://docs.docker.com/docker-hub/repos/manage/webhooks/ (example payload):
// push_data.tag is the pushed tag, repository.repo_name is "namespace/name".
type dockerHubPayload struct {
	PushData *struct {
		Tag string `json:"tag"`
	} `json:"push_data"`
	Repository *struct {
		RepoName string `json:"repo_name"`
	} `json:"repository"`
}

// ghPackage is the package object of GitHub's `package` and `registry_package`
// events: https://docs.github.com/en/webhooks/webhook-events-and-payloads#package
// and #registry_package. For a container image `namespace` is the owner and
// `name` the image name; package_version.container_metadata.tag.name is the
// pushed tag (empty for an untagged manifest).
type ghPackage struct {
	Name           string `json:"name"`
	Namespace      string `json:"namespace"`
	PackageVersion *struct {
		ContainerMetadata *struct {
			Tag struct {
				Name string `json:"name"`
			} `json:"tag"`
		} `json:"container_metadata"`
	} `json:"package_version"`
}

type ghPayload struct {
	Action          string     `json:"action"`
	Package         *ghPackage `json:"package"`
	RegistryPackage *ghPackage `json:"registry_package"`
}

// parsePayload recognises a registry delivery. It returns nil for a call that
// carries no registry payload (a plain POST from curl or CI), so the caller
// deploys the service. headers carries X-GitHub-Event.
func parsePayload(h http.Header, body []byte) *registryPush {
	ghEvent := strings.ToLower(h.Get("X-GitHub-Event"))
	if ghEvent == "ping" {
		return &registryPush{Source: SourceGHCR, Ignore: "GitHub ping: the webhook is reachable"}
	}
	if len(body) == 0 {
		return nil
	}
	if ghEvent == "package" || ghEvent == "registry_package" || ghEvent == "" {
		var p ghPayload
		if json.Unmarshal(body, &p) == nil {
			pkg := p.Package
			if ghEvent == "registry_package" || pkg == nil {
				pkg = p.RegistryPackage
			}
			if pkg != nil {
				return ghPush(p.Action, pkg)
			}
		}
	}
	if ghEvent != "" {
		// A GitHub event the hook does not use (a webhook subscribed to everything).
		return &registryPush{Source: SourceGHCR, Ignore: "GitHub event " + ghEvent + " is not a package event"}
	}
	var d dockerHubPayload
	if json.Unmarshal(body, &d) == nil && d.PushData != nil && d.Repository != nil && d.Repository.RepoName != "" {
		img, err := parseImage(d.Repository.RepoName)
		if err != nil {
			return &registryPush{Source: SourceDockerHub, Ignore: "the payload's repository name is not valid"}
		}
		if d.PushData.Tag == "" {
			return &registryPush{Source: SourceDockerHub, Image: img, Ignore: "the push carries no tag"}
		}
		return &registryPush{Source: SourceDockerHub, Image: img, Tag: d.PushData.Tag}
	}
	return nil
}

func ghPush(action string, pkg *ghPackage) *registryPush {
	out := &registryPush{Source: SourceGHCR}
	if action != "published" && action != "updated" {
		out.Ignore = "package action " + action + " does not publish an image"
		return out
	}
	if pkg.PackageVersion == nil || pkg.PackageVersion.ContainerMetadata == nil {
		out.Ignore = "the package is not a container image"
		return out
	}
	img, err := parseImage(hostGHCR + "/" + pkg.Namespace + "/" + pkg.Name)
	if err != nil {
		out.Ignore = "the payload's package name is not valid"
		return out
	}
	out.Image = img
	out.Tag = pkg.PackageVersion.ContainerMetadata.Tag.Name
	if out.Tag == "" {
		out.Ignore = "the push carries no tag (an untagged manifest)"
	}
	return out
}
