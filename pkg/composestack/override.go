package composestack

import (
	"bytes"
	"fmt"
	"sort"
	"strconv"
	"strings"

	"go.yaml.in/yaml/v3"

	"github.com/nebulactrl/nebulactrl/pkg/agentprotocol"
)

// Attachment is a network every component joins besides the project's own.
// The networks already exist (the agent ensures them before `compose up`) and
// are declared external in the override.
type Attachment struct {
	// Name is the Docker network name (nebulactrl-env-<id8>, ...-int,
	// nebulactrl-shared-<id8>).
	Name string
	// AliasSuffix, when set, makes the alias of a component on this network
	// "<component>.<suffix>" (team shared networks); empty means the alias is
	// the component name.
	AliasSuffix string
}

// OverrideInput is everything the override depends on.
type OverrideInput struct {
	ServiceID     string
	DeploymentID  string
	ReleaseID     string
	EnvironmentID string
	// Attachments are the networks of the environment (the first one is the
	// primary, the one Traefik routes on).
	Attachments []Attachment
	// Isolate makes the attachments the components' only networks: the
	// project's default network and the compose file's own networks are
	// dropped (a service that blocks outbound internet).
	Isolate bool
	// Resources are per-component limits that replace the compose file's.
	Resources map[string]agentprotocol.StackResources
	// Images pins components to image references (rollback).
	Images map[string]string
}

// Override is the generated override file.
type Override struct {
	// YAML is the compose override, passed as the second -f file.
	YAML []byte
	// Notes are warnings about the compose file for the deploy log.
	Notes []string
}

// BuildOverride generates the override that makes a compose project a
// NebulaCtrl stack: our labels and fixed container names, the environment
// network with a DNS alias per component, no host port publishing (routes are
// Traefik's job), per-component resource limits and pinned images. It never
// weakens the model: it only adds keys or removes ports and limits.
func BuildOverride(p *Project, in OverrideInput) (Override, error) {
	if in.ServiceID == "" || in.DeploymentID == "" {
		return Override{}, fmt.Errorf("override needs the service and deployment IDs")
	}
	var notes []string
	svcNodes := newMap()
	comps := p.Components()
	for _, c := range comps {
		sv := p.services[c.Name]
		m := newMap()
		m.set("container_name", scalar(agentprotocol.StackContainerName(in.ServiceID, c.Name)))

		labels := newMap()
		labels.set(agentprotocol.LabelManaged, scalar("true"))
		labels.set(agentprotocol.LabelService, scalar(in.ServiceID))
		labels.set(agentprotocol.LabelDeployment, scalar(in.DeploymentID))
		if in.ReleaseID != "" {
			labels.set(agentprotocol.LabelRelease, scalar(in.ReleaseID))
		}
		if in.EnvironmentID != "" {
			labels.set(agentprotocol.LabelEnvironment, scalar(in.EnvironmentID))
		}
		labels.set(agentprotocol.LabelComponent, scalar(c.Name))
		labels.set(agentprotocol.LabelReplica, scalar(strconv.Itoa(c.Index)))
		m.set("labels", labels.node())

		if sv["restart"] == nil {
			m.set("restart", scalar("unless-stopped"))
		}
		if img := in.Images[c.Name]; img != "" {
			m.set("image", scalar(img))
			m.set("pull_policy", scalar("missing"))
		}
		if len(asList(sv["ports"])) > 0 {
			m.set("ports", tagged("!reset", &yaml.Node{Kind: yaml.SequenceNode, Tag: "!!seq"}))
			notes = append(notes, fmt.Sprintf("%s: ports are not published on the host; point a domain at a container port to route it", c.Name))
		}
		if len(c.Profiles) > 0 {
			notes = append(notes, fmt.Sprintf("%s: belongs to the profiles %s and is not started unless COMPOSE_PROFILES selects one", c.Name, strings.Join(c.Profiles, ", ")))
		}
		if mode := asString(sv["network_mode"]); mode != "" {
			notes = append(notes, fmt.Sprintf("%s: network_mode %s: the service has no alias on the environment network and cannot be routed", c.Name, mode))
		} else if len(in.Attachments) > 0 {
			m.set("networks", serviceNetworks(sv, c.Name, in))
		}
		if r, ok := in.Resources[c.Name]; ok && (r.MemoryMB > 0 || r.CPUs > 0) {
			limits := newMap()
			if r.MemoryMB > 0 {
				limits.set("memory", scalar(strconv.Itoa(r.MemoryMB)+"M"))
				if sv["mem_limit"] != nil {
					m.set("mem_limit", tagged("!reset", &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!null", Value: "null"}))
				}
			}
			if r.CPUs > 0 {
				limits.set("cpus", scalar(strconv.FormatFloat(r.CPUs, 'f', -1, 64)))
				if sv["cpus"] != nil {
					m.set("cpus", tagged("!reset", &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!null", Value: "null"}))
				}
			}
			res := newMap()
			res.set("limits", limits.node())
			dep := newMap()
			dep.set("resources", res.node())
			m.set("deploy", dep.node())
		}
		svcNodes.set(c.Name, m.node())
	}

	root := newMap()
	root.set("services", svcNodes.node())
	if len(in.Attachments) > 0 {
		nets := newMap()
		for i, a := range in.Attachments {
			n := newMap()
			n.set("name", scalar(a.Name))
			n.set("external", &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!bool", Value: "true"})
			nets.set(netKey(i), n.node())
		}
		root.set("networks", nets.node())
	}

	var buf bytes.Buffer
	buf.WriteString("# Generated by NebulaCtrl for deployment " + in.DeploymentID + ". Do not edit.\n")
	enc := yaml.NewEncoder(&buf)
	enc.SetIndent(2)
	if err := enc.Encode(root.node()); err != nil {
		return Override{}, fmt.Errorf("encode override: %w", err)
	}
	if err := enc.Close(); err != nil {
		return Override{}, err
	}
	sort.Strings(notes)
	return Override{YAML: buf.Bytes(), Notes: notes}, nil
}

// netKey is the compose network key of the i-th attachment.
func netKey(i int) string { return "nc_net_" + strconv.Itoa(i) }

// serviceNetworks is the networks value of one component: the attachments
// with the component's alias, plus (unless isolating) the networks the service
// already has, or the project's default network when it had none.
func serviceNetworks(sv map[string]any, name string, in OverrideInput) *yaml.Node {
	m := newMap()
	if !in.Isolate && sv["networks"] == nil {
		m.set("default", &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map", Style: yaml.FlowStyle})
	}
	for i, a := range in.Attachments {
		alias := name
		if a.AliasSuffix != "" {
			alias = name + "." + a.AliasSuffix
		}
		aliases := &yaml.Node{Kind: yaml.SequenceNode, Tag: "!!seq", Style: yaml.FlowStyle, Content: []*yaml.Node{scalar(alias)}}
		e := newMap()
		e.set("aliases", aliases)
		m.set(netKey(i), e.node())
	}
	if in.Isolate {
		return tagged("!override", m.node())
	}
	return m.node()
}

// ---- ordered yaml node builder ----

type omap struct{ n *yaml.Node }

func newMap() *omap { return &omap{n: &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map"}} }

func (m *omap) set(key string, v *yaml.Node) {
	m.n.Content = append(m.n.Content, &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: key}, v)
}

func (m *omap) node() *yaml.Node { return m.n }

func scalar(s string) *yaml.Node {
	return &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: s, Style: yaml.DoubleQuotedStyle}
}

// tagged sets a compose merge tag (!reset, !override) on a node.
func tagged(tag string, n *yaml.Node) *yaml.Node {
	n.Tag = tag
	if n.Kind == yaml.SequenceNode {
		n.Style = yaml.FlowStyle
	}
	return n
}
