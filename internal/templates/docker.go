package templates

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"

	"go.yaml.in/yaml/v3"

	"github.com/nebulactrl/nebulactrl/pkg/agentprotocol"
)

// DockerBlock is the `docker:` block of a template service. It is exactly the
// service's Docker options: the keys are the JSON names of
// agentprotocol.DockerOptions (capAdd, shmSizeMb, ulimits, entrypoint…), so a
// new option reaches templates without a second field list. YAML decoding is
// strict: an unknown key is an error.
type DockerBlock struct {
	agentprotocol.DockerOptions
}

// UnmarshalYAML decodes the block through JSON so the key names and types are
// the ones of the service API.
func (d *DockerBlock) UnmarshalYAML(n *yaml.Node) error {
	var generic any
	if err := n.Decode(&generic); err != nil {
		return err
	}
	raw, err := json.Marshal(generic)
	if err != nil {
		return fmt.Errorf("docker: %w", err)
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	var o agentprotocol.DockerOptions
	if err := dec.Decode(&o); err != nil {
		return fmt.Errorf("line %d: docker: %w", n.Line, err)
	}
	if err := dec.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return fmt.Errorf("line %d: docker: unexpected trailing data", n.Line)
	}
	d.DockerOptions = o
	return nil
}

// MarshalYAML renders the block in block style with the field order of the
// options struct.
func (d DockerBlock) MarshalYAML() (any, error) {
	raw, err := json.Marshal(d.DockerOptions)
	if err != nil {
		return nil, err
	}
	var n yaml.Node
	if err := yaml.Unmarshal(raw, &n); err != nil {
		return nil, err
	}
	unflow(&n)
	if len(n.Content) == 1 {
		return n.Content[0], nil
	}
	return &n, nil
}

// unflow turns the flow style of a JSON-parsed node tree into block style;
// multi-line strings (shell scripts) become literal blocks.
func unflow(n *yaml.Node) {
	n.Style &^= yaml.FlowStyle
	if n.Kind == yaml.ScalarNode && n.Tag == "!!str" {
		n.Style = 0
		if strings.Contains(n.Value, "\n") {
			n.Style = yaml.LiteralStyle
		}
	}
	for _, c := range n.Content {
		unflow(c)
	}
}

// mapDockerStrings applies f to every string value of the options (map keys
// are left alone). Going through JSON keeps it complete for every option,
// including ones added later.
func mapDockerStrings(o *agentprotocol.DockerOptions, f func(string) string) (*agentprotocol.DockerOptions, error) {
	raw, err := json.Marshal(o)
	if err != nil {
		return nil, err
	}
	var generic any
	if err := json.Unmarshal(raw, &generic); err != nil {
		return nil, err
	}
	var walk func(v any) any
	walk = func(v any) any {
		switch x := v.(type) {
		case string:
			return f(x)
		case []any:
			for i := range x {
				x[i] = walk(x[i])
			}
		case map[string]any:
			for k := range x {
				x[k] = walk(x[k])
			}
		}
		return v
	}
	raw, err = json.Marshal(walk(generic))
	if err != nil {
		return nil, err
	}
	var out agentprotocol.DockerOptions
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// expandDockerInputs returns the options with ${input:KEY} expanded in every
// text value (entrypoint arguments, the user, group ids, labels…). vals are the
// install input values.
func expandDockerInputs(o *agentprotocol.DockerOptions, vals map[string]string) *agentprotocol.DockerOptions {
	if o == nil {
		return nil
	}
	out, err := mapDockerStrings(o, func(s string) string { return expandInputs(s, vals, false) })
	if err != nil {
		return o
	}
	return out
}

// docker checks one service's block with the same validator as the service's
// Docker options. It returns whether the block needs an unrestricted server.
// Input macros are replaced by a placeholder fitting the input (its default,
// or a stand-in for its format) so a number-typed field validates.
func (c *checker) docker(at string, s *TemplateService, inputs map[string]TemplateInput) (needsUnrestricted bool) {
	if s.Docker == nil {
		return false
	}
	if s.Database != nil {
		c.add(at + "docker options need an image service, not a managed database")
		return false
	}
	opts := s.Docker.DockerOptions
	if opts.IsZero() {
		c.add(at + "docker block is empty")
		return false
	}
	if _, err := mapDockerStrings(&opts, func(v string) string {
		c.macros(at+"docker", v, inputs)
		for _, m := range inputRe.FindAllStringSubmatch(v, -1) {
			if inputs[m[1]].Secret {
				c.addf("%sdocker: secret input %s would be stored in clear text in the service configuration", at, m[1])
			}
		}
		return v
	}); err != nil {
		c.addf("%sdocker: %v", at, err)
		return false
	}

	stand := map[string]string{}
	for k, in := range inputs {
		stand[k] = standIn(in)
	}
	probe := expandDockerInputs(&opts, stand)
	if err := probe.Validate(true); err != nil {
		c.addf("%sdocker: %v", at, err)
		return false
	}
	var files, vols []string
	for _, f := range s.Files {
		files = append(files, f.Path)
	}
	for _, v := range s.Volumes {
		vols = append(vols, v.Path)
	}
	if err := probe.ValidateFiles(files, vols); err != nil {
		c.addf("%sdocker: %v", at, err)
	}
	return probe.NeedsUnrestricted()
}

// standIn is a value that validates wherever the input is used.
func standIn(in TemplateInput) string {
	if in.Default != "" && !strings.Contains(in.Default, "${{") {
		return in.Default
	}
	switch {
	case len(in.Options) > 0:
		return in.Options[0]
	case in.Format == "number":
		return strconv.Itoa(1000)
	case in.Format == "hostname":
		return "example.com"
	}
	return "x"
}
