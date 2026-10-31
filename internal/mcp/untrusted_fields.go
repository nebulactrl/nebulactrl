package mcp

import "github.com/nebulactrl/nebulactrl/internal/mcp/mcpkit"

// Strings that come from people outside the team (a pull request's title and
// author, a commit's message, author and branch) can carry instructions meant
// for the model. Log lines are wrapped as untrusted blocks (wrapLogResponse);
// this wraps these fields the same way, in every operation result, so an
// agent reads them as data between nonce-marked boundaries.

// thirdPartyKeys are properties whose string value is third-party text.
var thirdPartyKeys = map[string]bool{"prTitle": true, "prAuthor": true, "headBranch": true}

// commitKeys are the third-party properties of a commit object.
var commitKeys = map[string]bool{"message": true, "author": true, "branch": true}

// wrapThirdParty replaces third-party strings in v (a decoded JSON result) by
// untrusted blocks and returns it. opID names the source of the text.
func wrapThirdParty(opID string, v any) any {
	switch t := v.(type) {
	case map[string]any:
		kind, _ := t["kind"].(string)
		for k, val := range t {
			switch s, isStr := val.(string); {
			case isStr && s != "" && (thirdPartyKeys[k] || (k == "name" && kind == "preview")):
				// A preview environment is named after the pull request.
				t[k] = mcpkit.WrapText(opID+":"+k, s)
			case k == "commit":
				if c, ok := val.(map[string]any); ok {
					for ck, cv := range c {
						if cs, ok := cv.(string); ok && cs != "" && commitKeys[ck] {
							c[ck] = mcpkit.WrapText(opID+":commit."+ck, cs)
						}
					}
				}
			default:
				t[k] = wrapThirdParty(opID, val)
			}
		}
		return t
	case []any:
		for i, e := range t {
			t[i] = wrapThirdParty(opID, e)
		}
	}
	return v
}
