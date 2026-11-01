package mcp

import (
	"context"
	"fmt"
	"strconv"
	"time"

	"github.com/google/uuid"

	"github.com/nebulactrl/nebulactrl/internal/deployments"
	"github.com/nebulactrl/nebulactrl/internal/mcp/mcpkit"
	"github.com/nebulactrl/nebulactrl/internal/secretgen"
)

// opHook adapts one operation's flat tool schema and request body to what a
// model needs and the REST layer wants.
type opHook struct {
	// pre may rewrite the arguments before the request is built (it can call
	// the API through e.get).
	pre func(ctx context.Context, e *env, args map[string]any) error
	// schema edits the flat input schema (adds MCP-only arguments).
	schema func(input jsonObj)
	// body rewrites the request body built from the arguments.
	body func(body map[string]any) error
	// result post-processes the (projected) response value.
	result func(ctx context.Context, e *env, v any) (any, error)
}

// opHooks keys hooks by operationId.
var opHooks = map[string]*opHook{
	// variable_set: `generate {length, charset}` asks the platform to invent a
	// secret, so the value never passes through the conversation. The REST
	// layer already runs `${{secret(n, alphabet)}}` generators once on write
	// and forces isSecret.
	"putVariables": {schema: addGenerateArg, body: expandGenerate},
	// deployment_get: `terminal` says whether polling can stop, and a
	// deployment whose deploy command was acknowledged but whose server went
	// offline fails with outcome_uncertain instead of looking merely slow.
	"getDeployment": {result: deploymentResult},
	// service_update: the REST `config` replaces the whole config and resets
	// omitted fields to defaults, which would silently undo settings when a
	// model sends only the field it means to change. The tool merges the given
	// fields into the current config instead.
	"updateService": {pre: mergeServiceConfig},
}

const (
	defaultGenerateLength = 32
	maxGenerateLength     = secretgen.MaxSecretLen
)

func addGenerateArg(input jsonObj) {
	props, _ := input["properties"].(jsonObj)
	vars, _ := props["variables"].(jsonObj)
	items, _ := vars["items"].(jsonObj)
	iprops, _ := items["properties"].(jsonObj)
	if iprops == nil {
		return
	}
	iprops["generate"] = jsonObj{
		"type":                 "object",
		"description":          "Ask the platform to invent the secret instead of passing a value. The generated value is never returned. Mutually exclusive with value.",
		"additionalProperties": false,
		"properties": jsonObj{
			"length": jsonObj{
				"type": "integer", "minimum": 1, "maximum": maxGenerateLength, "default": defaultGenerateLength,
				"description": fmt.Sprintf("characters, default %d", defaultGenerateLength),
			},
			"charset": jsonObj{
				"type": "string", "enum": toAnySlice(secretgen.PresetNames()),
				"description": "alphabet preset; default alnum",
			},
		},
	}
}

// expandGenerate rewrites variables[].generate to a `${{secret(n[, alphabet])}}`
// value and marks the variable secret.
func expandGenerate(body map[string]any) error {
	list, _ := body["variables"].([]any)
	for i, e := range list {
		item, ok := e.(map[string]any)
		if !ok {
			continue
		}
		gen, has := item["generate"]
		if !has {
			continue
		}
		delete(item, "generate")
		if _, hasValue := item["value"]; hasValue {
			return mcpkit.Errf(mcpkit.CodeInvalidArgument, "variables[%d]: pass either value or generate, not both", i)
		}
		g, _ := gen.(map[string]any)
		length := defaultGenerateLength
		if n, ok := g["length"]; ok {
			s, _ := scalarString(n)
			v, err := strconv.Atoi(s)
			if err != nil || v < 1 || v > maxGenerateLength {
				return mcpkit.Errf(mcpkit.CodeInvalidArgument, "variables[%d].generate.length must be 1..%d", i, maxGenerateLength)
			}
			length = v
		}
		expr := fmt.Sprintf("${{secret(%d)}}", length)
		if cs, _ := g["charset"].(string); cs != "" && cs != secretgen.AlphaAlnum {
			if _, ok := secretgen.Preset(cs); !ok {
				return mcpkit.Errf(mcpkit.CodeInvalidArgument, "variables[%d].generate.charset must be one of %v", i, secretgen.PresetNames())
			}
			expr = fmt.Sprintf("${{secret(%d, %s)}}", length, cs)
		}
		item["value"] = expr
		item["isSecret"] = true
	}
	return nil
}

func deploymentResult(ctx context.Context, e *env, v any) (any, error) {
	m, ok := v.(map[string]any)
	if !ok {
		return v, nil
	}
	status, _ := m["status"].(string)
	terminal := deployments.Status(status).IsTerminal()
	m["terminal"] = terminal
	wf, _ := m["waitingFor"].(map[string]any)
	if terminal || wf == nil {
		return m, nil
	}
	id, err := uuid.Parse(fmt.Sprint(m["id"]))
	if err != nil {
		return m, nil //nolint:nilerr // the id is taken from the server's own deployment view, so a non-UUID cannot occur; the hook only annotates results and must not turn a successful call into an error
	}
	if !e.commandAcked(ctx, id) {
		return m, nil
	}
	name, _ := wf["serverName"].(string)
	since, _ := wf["offlineSince"].(string)
	if _, err := time.Parse(time.RFC3339, since); err != nil {
		since = "unknown"
	}
	te := mcpkit.Errf(mcpkit.CodeOutcomeUncertain,
		"The agent acknowledged the deployment but server %q is offline (since %s); the outcome is unknown until it reconnects. Do not retry blindly.", name, since)
	te.Data = m
	return nil, te
}

// mergeServiceConfig turns a partial `config` argument into the full config:
// the service's current config with the given fields replaced. The merge is
// shallow, so `processes` replaces the whole list: there is no keyed merge, a
// caller sends every process it wants to keep.
func mergeServiceConfig(ctx context.Context, e *env, args map[string]any) error {
	cfg, ok := args["config"].(map[string]any)
	if !ok {
		return nil
	}
	sel, _ := args["serviceId"].(string)
	id, err := e.h.resolveService(ctx, sel)
	if err != nil {
		return err
	}
	cur, err := e.get(ctx, "/services/"+id.String())
	if err != nil {
		return err
	}
	merged := map[string]any{}
	if c, ok := cur["config"].(map[string]any); ok {
		for k, v := range c {
			merged[k] = v
		}
	}
	for k, v := range cfg {
		merged[k] = v
	}
	args["config"] = merged
	args["serviceId"] = id.String()
	return nil
}
