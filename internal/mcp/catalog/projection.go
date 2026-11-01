package catalog

import "strings"

// Projection trims a decoded JSON response (the value json.Unmarshal produces
// into any) for the model. It runs on the REST response of a curated tool
// before the 64 KB cap, so large views stay readable and secrets-adjacent
// fields never reach the transcript by accident.
//
// Paths are dotted object keys. Arrays are traversed transparently:
// "domains.hostname" selects the hostname of every element of "domains".
type Projection struct {
	// ItemsPath names the top-level key holding the list in a page object
	// ("items"). Empty means the whole response is the value to project (an
	// object, or an array of objects). Sibling keys of the list (nextCursor)
	// are kept as they are.
	ItemsPath string
	// Concise lists the paths kept per item when detail is concise. Selecting
	// a path keeps its whole subtree. Nil keeps everything.
	Concise []string
	// Omit lists paths removed at every detail level (large or low-value
	// fields).
	Omit []string
	// Hook runs last, on the already selected value, with the requested
	// detail. It may replace the value (variable listings use it to guarantee
	// that no secret value survives and to hide values unless detail=full).
	Hook func(detail Detail, v any) any
}

// Apply returns the projected value. v is consumed: callers must not reuse it.
func (p *Projection) Apply(detail Detail, v any) any {
	if p == nil {
		return v
	}
	if p.ItemsPath != "" {
		m, ok := v.(map[string]any)
		if !ok {
			return p.finish(detail, v)
		}
		if items, ok := m[p.ItemsPath]; ok {
			m[p.ItemsPath] = p.project(detail, items)
		}
		return p.finish(detail, m)
	}
	return p.finish(detail, p.project(detail, v))
}

func (p *Projection) project(detail Detail, v any) any {
	for _, path := range p.Omit {
		omit(v, strings.Split(path, "."))
	}
	if detail != DetailFull && len(p.Concise) > 0 {
		v = pick(v, buildTree(p.Concise))
	}
	return v
}

func (p *Projection) finish(detail Detail, v any) any {
	if p.Hook != nil {
		return p.Hook(detail, v)
	}
	return v
}

type pathNode struct {
	leaf     bool
	children map[string]*pathNode
}

func buildTree(paths []string) *pathNode {
	root := &pathNode{children: map[string]*pathNode{}}
	for _, path := range paths {
		n := root
		parts := strings.Split(path, ".")
		for i, part := range parts {
			c, ok := n.children[part]
			if !ok {
				c = &pathNode{children: map[string]*pathNode{}}
				n.children[part] = c
			}
			if i == len(parts)-1 {
				c.leaf = true
			}
			n = c
		}
	}
	return root
}

func pick(v any, n *pathNode) any {
	switch t := v.(type) {
	case []any:
		out := make([]any, len(t))
		for i, e := range t {
			out[i] = pick(e, n)
		}
		return out
	case map[string]any:
		out := make(map[string]any, len(n.children))
		for k, c := range n.children {
			val, ok := t[k]
			if !ok {
				continue
			}
			if c.leaf {
				out[k] = val
				continue
			}
			out[k] = pick(val, c)
		}
		return out
	default:
		return v
	}
}

func omit(v any, path []string) {
	switch t := v.(type) {
	case []any:
		for _, e := range t {
			omit(e, path)
		}
	case map[string]any:
		if len(path) == 1 {
			delete(t, path[0])
			return
		}
		if child, ok := t[path[0]]; ok {
			omit(child, path[1:])
		}
	}
}

// scrubSecretValues removes "value" from every object that is marked
// isSecret (or computedSecret) so a projection can never leak one, whatever
// the REST layer returned.
func scrubSecretValues(v any) any {
	switch t := v.(type) {
	case []any:
		for i, e := range t {
			t[i] = scrubSecretValues(e)
		}
	case map[string]any:
		if s, _ := t["isSecret"].(bool); s {
			delete(t, "value")
		}
		if s, _ := t["computedSecret"].(bool); s {
			delete(t, "value")
		}
		for k, e := range t {
			t[k] = scrubSecretValues(e)
		}
	}
	return v
}
