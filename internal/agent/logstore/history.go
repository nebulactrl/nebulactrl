package logstore

import (
	"strconv"
	"time"

	"github.com/nebulactrl/nebulactrl/internal/agent/logstore/query"
	"github.com/nebulactrl/nebulactrl/pkg/agentprotocol"
)

// Budget of one history query: how much it may decompress and how long it may
// run before it hands back a partial page and a cursor to continue.
const (
	queryScanBytes = int64(256) << 20
	queryDeadline  = 4 * time.Second
)

// Query answers a history query for one service: a page of the lines within
// the time range that match the query. Lines come in sequence order (newest
// first by default, which is what the cursor pages through); a replica read
// late can put a line slightly out of time order, so readers sort by time.
func (st *Store) Query(q agentprotocol.LogsQuery) (agentprotocol.LogsQueryResult, error) {
	limit := q.Limit
	if limit <= 0 {
		limit = agentprotocol.LogQueryDefaultLimit
	}
	limit = min(limit, agentprotocol.LogQueryMaxLimit)
	desc := q.Order != agentprotocol.LogOrderAsc

	match, perrs := query.Compile(q.Query)
	opts := ScanOptions{Desc: desc, MaxScanBytes: queryScanBytes, Deadline: time.Now().Add(queryDeadline)}
	if !q.From.IsZero() {
		opts.From = q.From.UnixNano()
	}
	if !q.To.IsZero() {
		opts.To = q.To.UnixNano()
	}
	if q.Cursor != "" {
		c, err := strconv.ParseUint(q.Cursor, 10, 64)
		if err != nil {
			return agentprotocol.LogsQueryResult{}, errBadCursor
		}
		opts.Cursor = c
	}
	res := agentprotocol.LogsQueryResult{Lines: []agentprotocol.HistoryLine{}}
	for _, e := range perrs {
		res.QueryErrors = append(res.QueryErrors, e.Message)
	}
	size := 0
	scan, err := st.Scan(q.ServiceID, opts, func(r Record) bool {
		l := query.NewLine(query.Input{
			Line: r.Text, Stream: agentprotocol.StreamRuntime, ServiceName: q.ServiceName,
			Replica: strconv.Itoa(r.Replica), DeploymentID: r.DeploymentID,
		})
		if !match(l) {
			return true
		}
		res.Lines = append(res.Lines, agentprotocol.HistoryLine{
			Seq: r.Seq, TS: time.Unix(0, r.TS).UTC(), Text: r.Text, Replica: r.Replica, DeploymentID: r.DeploymentID,
		})
		size += len(r.Text) + 96
		return len(res.Lines) < limit && size < agentprotocol.LogQueryMaxBytes
	})
	if err != nil {
		return res, err
	}
	if !scan.Done {
		res.NextCursor = strconv.FormatUint(scan.Cursor, 10)
	}
	res.Partial = scan.Partial
	b, err := st.Bounds(q.ServiceID)
	if err != nil {
		return res, err
	}
	if b.FirstSeq != 0 {
		oldest, newest := b.Oldest.UTC(), b.Newest.UTC()
		res.Oldest, res.Newest = &oldest, &newest
	}
	return res, nil
}

type queryError string

func (e queryError) Error() string { return string(e) }

const errBadCursor = queryError("logstore: invalid cursor")
