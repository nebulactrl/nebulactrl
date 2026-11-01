package agentprotocol

import (
	"bytes"
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestTunnelFrameRoundTrip(t *testing.T) {
	payload := bytes.Repeat([]byte{0, 1, 2, 0xff, '\n', '"', '{'}, 9000) // arbitrary bytes, no text encoding
	for _, tc := range []struct {
		name  string
		no    uint32
		flags byte
		data  []byte
	}{
		{"data", 7, 0, payload},
		{"fin with last bytes", 0xdeadbeef, TunnelFlagFIN, []byte("tail")},
		{"bare fin", 1, TunnelFlagFIN, nil},
		{"full chunk", 4294967295, 0, make([]byte, TunnelChunk)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			b := EncodeTunnelFrame(tc.no, tc.flags, tc.data)
			require.Len(t, b, TunnelHeaderLen+len(tc.data), "no base64, no JSON: header plus raw bytes")
			f, err := DecodeTunnelFrame(b)
			require.NoError(t, err)
			require.Equal(t, tc.no, f.No)
			require.Equal(t, tc.flags&TunnelFlagFIN != 0, f.FIN())
			require.True(t, bytes.Equal(tc.data, f.Payload))
		})
	}
}

func TestTunnelFrameLayoutIsFixed(t *testing.T) {
	// The wire format both ends (and any later agent) must keep.
	b := EncodeTunnelFrame(0x01020304, TunnelFlagFIN, []byte("ab"))
	require.Equal(t, []byte{0x01, 0x01, 0x01, 0x02, 0x03, 0x04, 'a', 'b'}, b)
	hdr := make([]byte, TunnelHeaderLen)
	PutTunnelHeader(hdr, 0x0a0b0c0d, 0)
	require.Equal(t, []byte{0x01, 0x00, 0x0a, 0x0b, 0x0c, 0x0d}, hdr)
}

func TestDecodeTunnelFrameRejectsMalformedInput(t *testing.T) {
	for name, b := range map[string][]byte{
		"empty":         nil,
		"short header":  {0x01, 0, 0, 0, 0},
		"unknown kind":  {0x02, 0, 0, 0, 0, 1},
		"json text":     []byte(`{"v":1,"type":"heartbeat"}`),
		"oversized":     append([]byte{0x01, 0, 0, 0, 0, 1}, make([]byte, TunnelChunk+1)...),
		"kind zero":     {0x00, 0, 0, 0, 0, 1},
		"all ones head": {0xff, 0xff, 0xff, 0xff, 0xff, 0xff},
	} {
		_, err := DecodeTunnelFrame(b)
		require.ErrorIs(t, err, ErrTunnelFrame, name)
	}
}

func TestTunnelControlFramesAreJSONWithEnvelope(t *testing.T) {
	b, err := Encode(TypeTunnelOpen, "", TunnelOpen{No: 3, Service: TunnelS3})
	require.NoError(t, err)
	f, err := Decode(b)
	require.NoError(t, err)
	require.Equal(t, TypeTunnelOpen, f.Type)
	var o TunnelOpen
	require.NoError(t, f.Into(&o))
	require.Equal(t, TunnelOpen{No: 3, Service: TunnelS3}, o)
	// Control frames belong to the stream family: critical in the agent outbox, dropped on disconnect.
	for _, typ := range []string{TypeTunnelOpen, TypeTunnelOpened, TypeTunnelAck, TypeTunnelClose} {
		require.True(t, IsStreamType(typ), typ)
	}
}

func TestEndpointsAreOptionalOnTheWire(t *testing.T) {
	// An older control plane's reconcile and an older agent's heartbeat carry neither field.
	var rec Reconcile
	require.NoError(t, json.Unmarshal([]byte(`{"services":[]}`), &rec))
	require.Nil(t, rec.Endpoints, "nil = unchanged")
	var hb Heartbeat
	require.NoError(t, json.Unmarshal([]byte(`{"cpuPercent":1}`), &hb))
	require.Empty(t, hb.Endpoints)

	b, err := json.Marshal(Reconcile{})
	require.NoError(t, err)
	require.NotContains(t, string(b), "endpoints")
	b, err = json.Marshal(Heartbeat{})
	require.NoError(t, err)
	require.NotContains(t, string(b), "endpoints")

	// An explicit empty set is not "unchanged": it stops every listener.
	b, err = json.Marshal(Reconcile{Endpoints: &InstanceEndpoints{Listen: []Endpoint{}}})
	require.NoError(t, err)
	require.Contains(t, string(b), `"endpoints":{"listen":[]}`)
}

func TestStandardEndpointsUseTheFixedLocalPorts(t *testing.T) {
	eps := StandardEndpoints()
	require.Equal(t, []Endpoint{
		{Service: TunnelS3, Port: 18333},
		{Service: TunnelRegistry, Port: 18500},
		{Service: TunnelLoki, Port: 18100},
	}, eps.Listen)
	require.Zero(t, LocalPort("grafana"))
	require.Zero(t, TunnelServiceLimit("grafana"))
	total := 0
	for _, s := range TunnelServices {
		total += TunnelServiceLimit(s)
	}
	require.GreaterOrEqual(t, total, MaxTunnelStreams, "the per-service caps leave the agent cap reachable")
}
