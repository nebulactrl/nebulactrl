package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/nebulactrl/nebulactrl/internal/auth"
	"github.com/nebulactrl/nebulactrl/internal/mcp/catalog"
	"github.com/nebulactrl/nebulactrl/internal/mcp/composites"
	"github.com/nebulactrl/nebulactrl/internal/mcp/mcpkit"
	"github.com/nebulactrl/nebulactrl/internal/policy"
)

// MaxListedTools caps how many tools one token sees in tools/list; everything
// else stays reachable through find_operation / call_operation.
const MaxListedTools = 80

// Tool kinds.
const (
	kindComposite = "composite"
	kindNative    = "native"
	kindMeta      = "meta"
	kindCurated   = "curated"
)

// callFn runs one tool. raw holds validated arguments (defaults applied).
type callFn func(ctx context.Context, e *env, req *mcp.CallToolRequest, raw json.RawMessage) (any, error)

// regTool is a tool as the registry lists it.
type regTool struct {
	name        string
	title       string
	description string
	toolset     string
	scopes      []string
	hints       mcpkit.Hints
	input       any
	output      any
	val         *compiled
	call        callFn
	kind        string
	opID        string
}

func (t *regTool) annotations() *mcp.ToolAnnotations {
	destructive := !t.hints.ReadOnly && t.hints.Destructive
	openWorld := t.hints.OpenWorld
	return &mcp.ToolAnnotations{
		ReadOnlyHint:    t.hints.ReadOnly,
		DestructiveHint: &destructive,
		IdempotentHint:  t.hints.Idempotent,
		OpenWorldHint:   &openWorld,
	}
}

// mcpTool renders the SDK tool definition.
func (t *regTool) mcpTool() *mcp.Tool {
	return &mcp.Tool{
		Name:         t.name,
		Description:  t.description,
		InputSchema:  t.input,
		OutputSchema: t.output,
		Annotations:  t.annotations(),
	}
}

// registry is the set of tools and operations of one handler.
type registry struct {
	tools []*regTool
	byOp  map[string]*opEntry
	// byName indexes tools.
	byName map[string]*regTool
}

// opEntry is an operation that is callable (or explicitly not) over MCP.
type opEntry struct {
	op    *opModel
	entry catalog.Entry
	// tool is the curated tool name, "" for generic and denied operations.
	tool string
}

func (o *opEntry) denied() (catalog.Deny, bool) {
	d, ok := o.entry.(catalog.Deny)
	return d, ok
}

// toolset returns the toolset of an allowed operation.
func (o *opEntry) toolset() string {
	switch e := o.entry.(type) {
	case catalog.Curated:
		return string(e.Toolset)
	case catalog.Generic:
		return string(e.Toolset)
	}
	return ""
}

func humanize(name string) string {
	words := strings.Split(name, "_")
	if len(words) > 0 && words[0] != "" {
		words[0] = strings.ToUpper(words[0][:1]) + words[0][1:]
	}
	return strings.Join(words, " ")
}

func hintsOf(h catalog.Hints) mcpkit.Hints {
	return mcpkit.Hints{ReadOnly: h.ReadOnly, Destructive: h.Destructive, Idempotent: h.Idempotent, OpenWorld: h.OpenWorld}
}

// buildRegistry assembles every tool: composites, native tools, the two meta
// tools and the curated primitives of the catalog. ops and manifest may be
// empty (before the API is attached).
func (h *Handler) buildRegistry(ops map[string]*opModel, manifest map[string]catalog.Entry) (*registry, error) {
	r := &registry{byOp: map[string]*opEntry{}, byName: map[string]*regTool{}}
	add := func(t *regTool) error {
		if _, dup := r.byName[t.name]; dup {
			return fmt.Errorf("duplicate tool name %q", t.name)
		}
		if t.val == nil {
			v, err := compileSchema(t.input)
			if err != nil {
				return fmt.Errorf("tool %s: %w", t.name, err)
			}
			t.val = v
			t.input = advertiseInput(t.input, "")
			if t.output != nil {
				t.output = advertiseOutput(t.output)
			}
		}
		r.byName[t.name] = t
		r.tools = append(r.tools, t)
		return nil
	}

	// 1. Composites.
	if h.deps.Services != nil {
		for _, ct := range composites.Tools(h.compositeDeps()) {
			t, err := fromKitTool(ct, kindComposite)
			if err != nil {
				return nil, err
			}
			if err := add(t); err != nil {
				return nil, err
			}
		}
	}

	// 2. Native tools (hand-written); a Curated{Native} catalog entry owns
	// their name, toolset, description and hints.
	natives := map[string]mcpkit.Tool{}
	for _, nt := range h.nativeTools() {
		natives[nt.Name] = nt
	}
	overlaid := map[string]bool{}
	ids := make([]string, 0, len(manifest))
	for id := range manifest {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	for _, id := range ids {
		if c, ok := manifest[id].(catalog.Curated); ok && c.Native {
			nt, found := natives[c.Name]
			if !found {
				return nil, fmt.Errorf("catalog entry %s names native tool %q which does not exist", id, c.Name)
			}
			nt.Toolset, nt.Description, nt.Hints = string(c.Toolset), c.Description, hintsOf(c.Hints)
			natives[c.Name] = nt
			overlaid[c.Name] = true
		}
	}
	names := make([]string, 0, len(natives))
	for n := range natives {
		names = append(names, n)
	}
	sort.Strings(names)
	for _, n := range names {
		t, err := fromKitTool(natives[n], kindNative)
		if err != nil {
			return nil, err
		}
		if err := add(t); err != nil {
			return nil, err
		}
	}

	// 3. Meta tools.
	for _, t := range h.metaTools(r) {
		if err := add(t); err != nil {
			return nil, err
		}
	}

	// 4. Operations: curated tools, generic and denied entries.
	for _, id := range ids {
		entry := manifest[id]
		op, ok := ops[id]
		if !ok {
			continue
		}
		oe := &opEntry{op: op, entry: entry}
		r.byOp[id] = oe
		c, isCurated := entry.(catalog.Curated)
		if !isCurated || c.Native {
			continue
		}
		oe.tool = c.Name
		t, err := h.curatedTool(op, c)
		if err != nil {
			return nil, err
		}
		if err := add(t); err != nil {
			return nil, err
		}
	}

	// Stable listing order: composites, natives, meta, then curated tools by
	// toolset and name.
	rank := map[string]int{kindComposite: 0, kindNative: 1, kindMeta: 2, kindCurated: 3}
	toolsetRank := map[string]int{}
	for i, ts := range mcpkit.Toolsets {
		toolsetRank[ts] = i
	}
	sort.SliceStable(r.tools, func(i, j int) bool {
		a, b := r.tools[i], r.tools[j]
		if rank[a.kind] != rank[b.kind] {
			return rank[a.kind] < rank[b.kind]
		}
		if a.kind == kindCurated {
			if toolsetRank[a.toolset] != toolsetRank[b.toolset] {
				return toolsetRank[a.toolset] < toolsetRank[b.toolset]
			}
			return a.name < b.name
		}
		return false
	})
	return r, nil
}

func fromKitTool(kt mcpkit.Tool, kind string) (*regTool, error) {
	if kt.Name == "" || kt.Call == nil {
		return nil, fmt.Errorf("tool %q has no name or Call", kt.Name)
	}
	if kt.Toolset == "" {
		return nil, fmt.Errorf("tool %s has no toolset", kt.Name)
	}
	in, err := schemaOf(kt.Input)
	if err != nil {
		return nil, fmt.Errorf("tool %s input schema: %w", kt.Name, err)
	}
	if in == nil {
		in = &jsonschemaEmpty
	}
	var out any
	if kt.Output != nil {
		os, err := schemaOf(kt.Output)
		if err != nil {
			return nil, fmt.Errorf("tool %s output schema: %w", kt.Name, err)
		}
		out = os
	}
	title := kt.Title
	if title == "" {
		title = humanize(kt.Name)
	}
	call := kt.Call
	return &regTool{
		name: kt.Name, title: title, description: tightDescription(kt.Description), toolset: kt.Toolset,
		scopes: append([]string{}, kt.Scopes...), hints: kt.Hints, input: in, output: out, kind: kind,
		call: func(ctx context.Context, e *env, _ *mcp.CallToolRequest, raw json.RawMessage) (any, error) {
			return call(ctx, e.p, raw)
		},
	}, nil
}

// visible lists the tools the principal may call, within the selected
// toolsets (nil = all), capped at MaxListedTools.
func (r *registry) visible(p *auth.Principal, toolsets map[string]bool) []*regTool {
	out := make([]*regTool, 0, len(r.tools))
	for _, t := range r.tools {
		if toolsets != nil && !toolsets[t.toolset] {
			continue
		}
		if !holdsAll(p, t.scopes) {
			continue
		}
		out = append(out, t)
		if len(out) >= MaxListedTools {
			break
		}
	}
	return out
}

// holdsAll reports whether p holds every scope (admin implies all).
func holdsAll(p *auth.Principal, scopes []string) bool {
	for _, s := range scopes {
		if !policy.HasScope(p, s) {
			return false
		}
	}
	return true
}

func missingScopes(p *auth.Principal, scopes []string) []string {
	var miss []string
	for _, s := range scopes {
		if !policy.HasScope(p, s) {
			miss = append(miss, s)
		}
	}
	return miss
}

// curatedTool builds the tool of a curated operation.
func (h *Handler) curatedTool(op *opModel, c catalog.Curated) (*regTool, error) {
	input := cloneJSON(op.input)
	if c.Projection != nil {
		props := input["properties"].(jsonObj)
		props["detail"] = jsonObj{
			"type":        "string",
			"enum":        []any{"concise", "full"},
			"default":     "concise",
			"description": "concise (default) returns the fields needed to decide the next step; full returns the whole record.",
		}
	}
	val, err := compileSchema(input)
	if err != nil {
		return nil, fmt.Errorf("tool %s: %w", c.Name, err)
	}
	t := &regTool{
		name: c.Name, title: humanize(c.Name), description: tightDescription(c.Description), toolset: string(c.Toolset),
		scopes: append([]string{}, op.scopes...), hints: hintsOf(c.Hints),
		input: advertiseInput(input, op.id), output: curatedOutput(op, c), val: val, kind: kindCurated, opID: op.id,
	}
	t.call = func(ctx context.Context, e *env, _ *mcp.CallToolRequest, raw json.RawMessage) (any, error) {
		args, err := decodeArgs(raw)
		if err != nil {
			return nil, err
		}
		detail := mcpkit.DetailConcise
		if s, _ := args["detail"].(string); s == "full" {
			detail = mcpkit.DetailFull
		}
		delete(args, "detail")
		return e.callOp(ctx, op, c.Projection, detail, args)
	}
	return t, nil
}

func cloneJSON(m jsonObj) jsonObj {
	raw, _ := json.Marshal(m)
	var out jsonObj
	_ = json.Unmarshal(raw, &out)
	return out
}

// ToolInfo describes a registered tool for docs, tests and diagnostics.
type ToolInfo struct {
	Name        string
	Title       string
	Description string
	Toolset     string
	Kind        string
	// Operation is the REST operationId of a curated tool.
	Operation string
	Scopes    []string
	Hints     mcpkit.Hints
}

func (t *regTool) info() ToolInfo {
	return ToolInfo{
		Name: t.name, Title: t.title, Description: t.description, Toolset: t.toolset, Kind: t.kind,
		Operation: t.opID, Scopes: append([]string{}, t.scopes...), Hints: t.hints,
	}
}

// Tools lists every registered tool, in listing order, regardless of who may
// see it.
func (h *Handler) Tools() []ToolInfo {
	reg := h.reg.Load()
	out := make([]ToolInfo, len(reg.tools))
	for i, t := range reg.tools {
		out[i] = t.info()
	}
	return out
}

// VisibleTools lists the tools a principal sees in tools/list, within the
// toolsets (nil = all).
func (h *Handler) VisibleTools(p *auth.Principal, toolsets ...string) []ToolInfo {
	var sel map[string]bool
	if len(toolsets) > 0 {
		sel = map[string]bool{}
		for _, t := range toolsets {
			sel[t] = true
		}
	}
	vis := h.reg.Load().visible(p, sel)
	out := make([]ToolInfo, len(vis))
	for i, t := range vis {
		out[i] = t.info()
	}
	return out
}

// curatedOutput is the advertised output schema of a generated tool: omitted
// for projected results (the shape depends on `detail`; structuredContent and
// the text mirror still carry the data), shallow otherwise.
func curatedOutput(op *opModel, c catalog.Curated) any {
	if c.Projection != nil || op.logs {
		return nil
	}
	return op.output
}

const (
	maxDescSentences = 3
	maxDescBytes     = 340
)

// tightDescription keeps a tool description to its first sentences (at most
// maxDescSentences and maxDescBytes): descriptions are part of every
// tools/list, so the full reference lives in the docs and find_operation.
func tightDescription(s string) string {
	s = strings.Join(strings.Fields(s), " ")
	if len(s) <= maxDescBytes && strings.Count(s, ". ") < maxDescSentences {
		return s
	}
	var out string
	n := 0
	rest := s
	for n < maxDescSentences {
		i := strings.Index(rest, ". ")
		if i < 0 {
			if out == "" || len(out)+len(rest) <= maxDescBytes {
				out += rest
			}
			return strings.TrimSpace(out)
		}
		sentence := rest[:i+1]
		if out != "" && len(out)+len(sentence)+1 > maxDescBytes {
			break
		}
		if out != "" {
			out += " "
		}
		out += sentence
		rest = rest[i+2:]
		n++
	}
	return strings.TrimSpace(out)
}
