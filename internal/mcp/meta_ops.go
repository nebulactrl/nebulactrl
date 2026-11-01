package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"unicode"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/nebulactrl/nebulactrl/internal/mcp/catalog"
	"github.com/nebulactrl/nebulactrl/internal/mcp/mcpkit"
)

const (
	findDefaultLimit = 5
	findMaxLimit     = 15
)

type findOperationInput struct {
	Query string `json:"query" jsonschema:"what you want to do, in words (e.g. 'rotate database credentials', 'list volumes', 'schedule backup')"`
	Tag   string `json:"tag,omitempty" jsonschema:"restrict to an API area, e.g. services, domains, backups, databases, servers"`
	Limit int    `json:"limit,omitempty" jsonschema:"maximum matches (default 5, max 15)"`
}

type operationInfo struct {
	OperationID string   `json:"operationId"`
	Method      string   `json:"method"`
	Path        string   `json:"path"`
	Summary     string   `json:"summary"`
	Description string   `json:"description,omitempty"`
	Tags        []string `json:"tags"`
	Scopes      []string `json:"scopes"`
	Toolset     string   `json:"toolset"`
	// Tool is the curated tool that wraps the operation, if any.
	Tool string `json:"tool,omitempty"`
	// Mutating is true when the call changes state.
	Mutating bool `json:"mutating"`
	// ProductionGuard is true when MCP calls on production need approval.
	ProductionGuard bool           `json:"productionGuard,omitempty"`
	InputSchema     map[string]any `json:"inputSchema"`
}

type findOperationOutput struct {
	Matches []operationInfo `json:"matches"`
	Total   int             `json:"total" jsonschema:"number of matching operations before the limit"`
	Note    string          `json:"note,omitempty"`
}

type callOperationInput struct {
	OperationID string         `json:"operationId" jsonschema:"the operationId from find_operation"`
	Arguments   map[string]any `json:"arguments,omitempty" jsonschema:"arguments as described by the operation's inputSchema (path, query and body fields in one flat object)"`
	Detail      string         `json:"detail,omitempty" jsonschema:"concise (default) or full, for operations with a curated tool"`
}

// metaTools returns find_operation and call_operation over registry r.
func (h *Handler) metaTools(r *registry) []*regTool {
	find := &regTool{
		name: "find_operation", title: "Find operation", toolset: mcpkit.ToolsetCore, kind: kindMeta,
		description: "Search every API operation you may call that has no dedicated tool (the long tail: schedules, drains, previews, certificates, DNS, backups settings and more). " +
			"Returns matches with their input schema and scopes; run one with call_operation. Denied operations (credential reveals, token minting, instance admin, host shell) are never listed.",
		hints: mcpkit.Hints{ReadOnly: true},
		input: mustSchema(findOperationInput{}), output: mustSchema(findOperationOutput{}),
		call: func(ctx context.Context, e *env, _ *mcp.CallToolRequest, raw json.RawMessage) (any, error) {
			in, err := mcpkit.Decode[findOperationInput](raw)
			if err != nil {
				return nil, err
			}
			return e.findOperations(r, in), nil
		},
	}
	call := &regTool{
		name: "call_operation", title: "Call operation", toolset: mcpkit.ToolsetCore, kind: kindMeta,
		description: "Call any allowed API operation by operationId (see find_operation). Arguments are validated against the operation's input schema; " +
			"path ids also accept selectors (project/env/service, hostnames). Runs under your token's scopes and project restriction; " +
			"changes to production services need approval. Edits only stage: they take effect through apply_changes. Denied operations cannot be called.",
		// Worst case of everything it can reach: the real hints belong to the operation.
		hints: mcpkit.Hints{Destructive: true, OpenWorld: true},
		input: mustSchema(callOperationInput{}),
		call: func(ctx context.Context, e *env, _ *mcp.CallToolRequest, raw json.RawMessage) (any, error) {
			in, err := mcpkit.Decode[callOperationInput](raw)
			if err != nil {
				return nil, err
			}
			return e.callOperation(ctx, r, in)
		},
	}
	return []*regTool{find, call}
}

func mustSchema(zero any) any {
	s, err := schemaOf(zero)
	if err != nil {
		panic(fmt.Sprintf("mcp: schema of %T: %v", zero, err))
	}
	return s
}

// callable returns the operation entry when the principal may call it.
func (e *env) callable(r *registry, id string) (*opEntry, error) {
	oe, ok := r.byOp[id]
	if !ok {
		return nil, mcpkit.Errf(mcpkit.CodeNotFound, "unknown operation %q; use find_operation to search", id)
	}
	if d, denied := oe.denied(); denied {
		return nil, mcpkit.Errf("operation_denied", "%s is not available over MCP (%s): %s", id, d.Reason, d.Why)
	}
	if ts := oe.toolset(); e.toolsets != nil && !e.toolsets[ts] {
		return nil, mcpkit.Errf(mcpkit.CodeForbidden, "operation %s is in the %q toolset, which this connection did not select (/mcp?toolsets=…)", id, ts)
	}
	if miss := missingScopes(e.p, oe.op.scopes); len(miss) > 0 {
		return nil, mcpkit.Errf(mcpkit.CodeForbidden, "operation %s needs the scopes %s, which this token does not hold", id, strings.Join(miss, ", "))
	}
	return oe, nil
}

func (e *env) callOperation(ctx context.Context, r *registry, in callOperationInput) (any, error) {
	id := strings.TrimSpace(in.OperationID)
	oe, err := e.callable(r, id)
	if err != nil {
		return nil, err
	}
	detail, err := mcpkit.ParseDetail(in.Detail)
	if err != nil {
		return nil, err
	}
	raw, err := json.Marshal(in.Arguments)
	if err != nil {
		return nil, mcpkit.Errf(mcpkit.CodeInvalidArgument, "arguments are not valid JSON")
	}
	validated, err := oe.op.validator.apply(raw)
	if err != nil {
		return nil, mcpkit.Errf(mcpkit.CodeInvalidArgument, "invalid arguments for %s: %s (see find_operation for its input schema)", id, cleanValidation(err))
	}
	args, err := decodeArgs(validated)
	if err != nil {
		return nil, err
	}
	var proj *catalog.Projection
	if c, ok := oe.entry.(catalog.Curated); ok {
		proj = c.Projection
	}
	return e.callOp(ctx, oe.op, proj, detail, args)
}

// findOperations ranks the operations the principal may call by how well they
// match the query.
func (e *env) findOperations(r *registry, in findOperationInput) findOperationOutput {
	limit := in.Limit
	switch {
	case limit <= 0:
		limit = findDefaultLimit
	case limit > findMaxLimit:
		limit = findMaxLimit
	}
	tokens := queryTokens(in.Query)
	tag := strings.ToLower(strings.TrimSpace(in.Tag))

	type hit struct {
		oe    *opEntry
		score int
	}
	var hits []hit
	for _, oe := range r.byOp {
		if _, denied := oe.denied(); denied {
			continue
		}
		if e.toolsets != nil && !e.toolsets[oe.toolset()] {
			continue
		}
		if !holdsAll(e.p, oe.op.scopes) {
			continue
		}
		if tag != "" && !hasTag(oe.op.tags, tag) {
			continue
		}
		score := matchScore(oe, tokens)
		if len(tokens) > 0 && score == 0 {
			continue
		}
		hits = append(hits, hit{oe, score})
	}
	sort.Slice(hits, func(i, j int) bool {
		if hits[i].score != hits[j].score {
			return hits[i].score > hits[j].score
		}
		return hits[i].oe.op.id < hits[j].oe.op.id
	})
	out := findOperationOutput{Matches: []operationInfo{}, Total: len(hits)}
	for i, h := range hits {
		if i >= limit {
			break
		}
		out.Matches = append(out.Matches, operationInfoOf(h.oe))
	}
	for len(out.Matches) > 1 && mcpkit.JSONSize(out) > MaxResultBytes-512 {
		out.Matches = out.Matches[:len(out.Matches)-1]
		out.Note = "matches reduced to fit the 64 KB result cap; narrow the query or pass a tag"
	}
	if len(out.Matches) == 0 {
		out.Note = "no allowed operation matches; try other words or a tag (" + strings.Join(r.tags(e), ", ") + ")"
	}
	return out
}

func operationInfoOf(oe *opEntry) operationInfo {
	m := oe.op
	info := operationInfo{
		OperationID: m.id, Method: m.method, Path: m.path, Summary: m.summary, Description: clipText(m.description, 600),
		Tags: m.tags, Scopes: m.scopes, Toolset: oe.toolset(), Tool: oe.tool, Mutating: m.mutating(), InputSchema: m.input,
	}
	switch c := oe.entry.(type) {
	case catalog.Curated:
		info.ProductionGuard = c.ProdGuard
	case catalog.Generic:
		info.ProductionGuard = c.ProdGuard
	}
	if info.Tags == nil {
		info.Tags = []string{}
	}
	return info
}

func clipText(s string, n int) string {
	if len(s) <= n {
		return s
	}
	cut := n
	for cut > 0 && s[cut]&0xC0 == 0x80 {
		cut--
	}
	return s[:cut] + "…"
}

// tags lists the API areas the principal can search.
func (r *registry) tags(e *env) []string {
	set := map[string]bool{}
	for _, oe := range r.byOp {
		if _, denied := oe.denied(); denied || !holdsAll(e.p, oe.op.scopes) {
			continue
		}
		for _, t := range oe.op.tags {
			set[t] = true
		}
	}
	out := make([]string, 0, len(set))
	for t := range set {
		out = append(out, t)
	}
	sort.Strings(out)
	return out
}

func hasTag(tags []string, tag string) bool {
	for _, t := range tags {
		if strings.EqualFold(t, tag) {
			return true
		}
	}
	return false
}

func queryTokens(q string) []string {
	f := strings.FieldsFunc(strings.ToLower(q), func(r rune) bool { return !unicode.IsLetter(r) && !unicode.IsDigit(r) })
	out := f[:0]
	for _, t := range f {
		if len(t) > 1 {
			out = append(out, t)
		}
	}
	return out
}

// matchScore counts query tokens found in the operation's id, summary, path,
// tags and description, weighting the id and summary most.
func matchScore(oe *opEntry, tokens []string) int {
	m := oe.op
	id := strings.ToLower(m.id)
	sum := strings.ToLower(m.summary)
	path := strings.ToLower(m.path)
	tags := strings.ToLower(strings.Join(m.tags, " "))
	desc := strings.ToLower(m.description)
	score := 0
	for _, t := range tokens {
		s := 0
		if strings.Contains(id, t) {
			s += 4
		}
		if strings.Contains(sum, t) {
			s += 3
		}
		if strings.Contains(path, t) || strings.Contains(tags, t) {
			s += 2
		}
		if strings.Contains(desc, t) {
			s++
		}
		score += s
	}
	return score
}
