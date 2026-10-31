//go:build !linux && !darwin

package hostpty

import "context"

// Session is a placeholder: pseudo terminals are not supported here.
type Session struct{}

// Start always fails with ErrUnsupported.
func Start(Spec) (*Session, error) { return nil, ErrUnsupported }

func (*Session) Read([]byte) (int, error)               { return 0, ErrUnsupported }
func (*Session) Write([]byte) (int, error)              { return 0, ErrUnsupported }
func (*Session) Resize(context.Context, int, int) error { return ErrUnsupported }
func (*Session) Wait(context.Context) (int, error)      { return -1, ErrUnsupported }
func (*Session) Close() error                           { return nil }
