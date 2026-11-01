package conn

// InflightCommands is how many commands are running right now (received and
// not finished). A self-upgrade waits for it to reach zero before restarting.
func (c *Client) InflightCommands() int {
	n := 0
	c.inflight.Range(func(_, _ any) bool { n++; return true })
	return n
}
