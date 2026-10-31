package gitproviders

import "time"

// SignForTest returns the hex HMAC-SHA256 webhook signature of body.
func SignForTest(secret string, body []byte) string { return hmacSHA256Hex(secret, body) }

// SetNow replaces the service clock.
func (s *Service) SetNow(fn func() time.Time) { s.now = fn }
