package engine

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"time"

	"github.com/nebulactrl/nebulactrl/internal/databases/engines"
	"github.com/nebulactrl/nebulactrl/pkg/agentprotocol"
)

// clickHouseOutputLimit caps the JSON text the agent reads back from
// clickhouse-client. The server-side result limits keep a result well below it;
// a result that still hits it is reported as too large rather than cut.
const clickHouseOutputLimit = 16 << 20

// queryClickHouse runs run.sql with clickhouse-client inside the container and
// collects every JSONCompact result into b.
func (e *Engine) queryClickHouse(ctx context.Context, containerID string, creds engines.Credentials, run dbRun, rd *redactor, b *dbResultBuilder) error {
	plan, err := engines.QueryPlan(creds, run.sql, run.allowWrites, run.timeout, run.maxRows)
	if err != nil {
		return err
	}
	out, truncated, err := e.execPlanLimit(ctx, containerID, plan, rd, run.timeout+10*time.Second, clickHouseOutputLimit)
	if err != nil {
		return err
	}
	if truncated {
		return errors.New("the result is too large to show; select fewer columns or rows")
	}
	return decodeClickHouseResults(out, b)
}

// clickHouseResult is one JSONCompact document.
type clickHouseResult struct {
	Meta []struct {
		Name string `json:"name"`
		Type string `json:"type"`
	} `json:"meta"`
	Data [][]json.RawMessage `json:"data"`
}

// decodeClickHouseResults reads the concatenated JSONCompact documents
// clickhouse-client prints for a multi-statement input (one per statement that
// returns data) into b.
func decodeClickHouseResults(out string, b *dbResultBuilder) error {
	dec := json.NewDecoder(strings.NewReader(out))
	for {
		var r clickHouseResult
		err := dec.Decode(&r)
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			return errors.New("unexpected output from clickhouse-client: " + err.Error())
		}
		cols := make([]agentprotocol.DBColumn, len(r.Meta))
		for i, m := range r.Meta {
			cols[i] = agentprotocol.DBColumn{Name: m.Name, Type: m.Type}
		}
		wanted := b.beginSet(cols)
		for _, row := range r.Data {
			if !wanted {
				break
			}
			vals := make([][]byte, len(row))
			for i, cell := range row {
				vals[i] = clickHouseCell(cell)
			}
			wanted = b.addRow(vals)
		}
		b.endSet("")
	}
}

// clickHouseCell is the text form of one JSON value: strings unquoted, numbers
// and booleans as written, null as NULL (nil), arrays and objects as JSON.
func clickHouseCell(raw json.RawMessage) []byte {
	raw = bytes.TrimSpace(raw)
	switch {
	case len(raw) == 0 || bytes.Equal(raw, []byte("null")):
		return nil
	case raw[0] == '"':
		var s string
		if err := json.Unmarshal(raw, &s); err == nil {
			return []byte(s)
		}
	}
	return []byte(raw)
}
