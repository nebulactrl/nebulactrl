package engine

// trafficSetter is implemented by proxy.Manager. It is optional so proxies
// that do not log access (tests) need no stub.
type trafficSetter interface{ SetTrafficAnalytics(enabled bool) }

// applyTrafficSetting passes the control plane's traffic analytics toggle to
// the proxy; the next route push renders (or omits) the access logging config.
// A nil value leaves the setting unchanged.
func (e *Engine) applyTrafficSetting(v *bool) {
	if v == nil {
		return
	}
	if p, ok := e.cfg.Proxy.(trafficSetter); ok {
		p.SetTrafficAnalytics(*v)
	}
}
