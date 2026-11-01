package main

import "testing"

func TestProbeAddr(t *testing.T) {
	for in, want := range map[string]string{
		"":              "127.0.0.1:8080",
		":8080":         "127.0.0.1:8080",
		"0.0.0.0:9000":  "127.0.0.1:9000",
		"[::]:9001":     "127.0.0.1:9001",
		"10.0.0.5:9002": "10.0.0.5:9002",
		"garbage":       "127.0.0.1:8080",
	} {
		if got := probeAddr(in); got != want {
			t.Errorf("probeAddr(%q) = %q, want %q", in, got, want)
		}
	}
}
