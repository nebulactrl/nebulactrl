package services

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestParseDownloadFormat(t *testing.T) {
	for in, want := range map[string]string{"": DownloadText, "txt": DownloadText, "TXT": DownloadText, " ndjson ": DownloadNDJSON} { //nolint:gocritic // the padded key deliberately checks that ParseDownloadFormat trims whitespace
		got, err := ParseDownloadFormat(in)
		require.NoError(t, err, in)
		require.Equal(t, want, got, in)
	}
	_, err := ParseDownloadFormat("csv")
	require.Error(t, err)
}

func TestDownloadFilename(t *testing.T) {
	at := time.Date(2026, 10, 28, 9, 5, 7, 0, time.FixedZone("x", 3600))
	require.Equal(t, "web-logs-20261028T080507Z.txt", DownloadFilename("web", "logs", at, DownloadText))
	require.Equal(t, "web-deploy-1a2b3c4d-20261028T080507Z.ndjson", DownloadFilename("web", "deploy-1a2b3c4d", at, DownloadNDJSON))
}

func TestLogDownloadFormats(t *testing.T) {
	ts := time.Date(2026, 1, 2, 3, 4, 5, 600_000_000, time.FixedZone("x", 7200))
	var txt bytes.Buffer
	d := NewLogDownloadTo(&txt, DownloadText)
	require.NoError(t, d.Write(DownloadLine{TS: ts, Stream: "runtime", Line: "hello\r\n"}))
	require.NoError(t, d.Write(DownloadLine{TS: ts, Stream: "build", Line: "step 1"}))
	d.Note("cut here")
	require.Equal(t, "2026-01-02T01:04:05.6Z hello\n2026-01-02T01:04:05.6Z [build] step 1\n# cut here\n", txt.String())
	require.Equal(t, 2, d.Lines())

	var nd bytes.Buffer
	d = NewLogDownloadTo(&nd, DownloadNDJSON)
	rep := 2
	require.NoError(t, d.Write(DownloadLine{TS: ts, Stream: "runtime", Replica: &rep, DeploymentID: "dep", Line: "hello"}))
	d.Note("cut here")
	lines := strings.Split(strings.TrimSpace(nd.String()), "\n")
	require.Len(t, lines, 2)
	require.JSONEq(t, `{"ts":"2026-01-02T01:04:05.6Z","stream":"runtime","replica":2,"deploymentId":"dep","line":"hello"}`, lines[0])
	var note struct {
		Note  string `json:"note"`
		Lines int    `json:"lines"`
	}
	require.NoError(t, json.Unmarshal([]byte(lines[1]), &note))
	require.Equal(t, "cut here", note.Note)
	require.Equal(t, 1, note.Lines)
}

func TestLogDownloadFullAtTheCap(t *testing.T) {
	var buf bytes.Buffer
	d := NewLogDownloadTo(&buf, DownloadText)
	for range MaxDownloadLines {
		require.False(t, d.Full())
		require.NoError(t, d.Write(DownloadLine{Line: "x"}))
	}
	require.True(t, d.Full())
}
