package api

import "context"

func contextWithoutCancel(ctx context.Context) context.Context { return context.WithoutCancel(ctx) }

type ctxKey int

const clientIPKey ctxKey = 1

// ClientIP returns the caller's IP address as seen by the server.
func ClientIP(ctx context.Context) string {
	s, _ := ctx.Value(clientIPKey).(string)
	return s
}
