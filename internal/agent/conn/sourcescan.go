package conn

import "github.com/nebulactrl/nebulactrl/pkg/agentprotocol"

// SourceScan reports the template env files found in a checked-out source.
func (c *Client) SourceScan(s agentprotocol.SourceScan) { c.send(agentprotocol.TypeSourceScan, "", s) }
