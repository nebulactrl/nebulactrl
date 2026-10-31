package conn

import "github.com/nebulactrl/nebulactrl/pkg/agentprotocol"

// LogStatus reports the log store's usage and the health of every drain. Only
// the latest queued report is kept.
func (c *Client) LogStatus(s agentprotocol.LogStatus) { c.send(agentprotocol.TypeLogStatus, "", s) }
