package databases

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"
	"unicode"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/nebulactrl/nebulactrl/internal/agents"
	"github.com/nebulactrl/nebulactrl/internal/apierr"
	"github.com/nebulactrl/nebulactrl/internal/audit"
	"github.com/nebulactrl/nebulactrl/internal/auth"
	"github.com/nebulactrl/nebulactrl/internal/databases/engines"
	"github.com/nebulactrl/nebulactrl/internal/db"
	"github.com/nebulactrl/nebulactrl/internal/policy"
	"github.com/nebulactrl/nebulactrl/internal/services"
	"github.com/nebulactrl/nebulactrl/pkg/agentprotocol"
)

// Audit actions of the Data tab. The statement text is never recorded.
const (
	ActionDatabaseQuery  = "database.query"
	ActionDatabaseBrowse = "database.browse"
)

// Error codes of the Data tab.
const (
	CodeDataNotSupported     = "data_not_supported"
	CodeServiceStopped       = "service_stopped"
	CodeNotDeployed          = "not_deployed"
	CodeQueryFailed          = "query_failed"
	CodeConfirmationRequired = "confirmation_required"
)

// Limits of the Data tab's requests. The agent applies them (and its own caps);
// the control plane always sends these values.
const (
	dataTimeoutSeconds = 30
	dataQueryMaxRows   = 1000
	dataBrowseMaxRows  = 100
	// dataRequestTimeout is how long the control plane waits for the agent: the
	// statement timeout plus connecting and encoding.
	dataRequestTimeout = 45 * time.Second
)

// DataRequester asks an agent a question and waits for the answer
// (*agents.Hub implements it).
type DataRequester interface {
	Request(ctx context.Context, serverID uuid.UUID, kind string, payload any, secrets map[string]string, timeout time.Duration) (agentprotocol.ResultFrame, error)
}

// SetRequester installs the channel to the agents the Data tab asks through.
func (s *Service) SetRequester(r DataRequester) { s.req = r }

// DataSupported reports whether the Data tab serves the engine: the SQL engines
// the agent can talk to.
func DataSupported(engine string) bool {
	switch engine {
	case engines.Postgres, engines.MySQL, engines.MariaDB, engines.ClickHouse:
		return true
	}
	return false
}

// DataTable is one table or view of the database.
type DataTable struct {
	Schema      string `json:"schema"`
	Name        string `json:"name"`
	Kind        string `json:"kind" enum:"table,view,other"`
	RowEstimate *int64 `json:"rowEstimate,omitempty" doc:"Approximate row count from the engine's statistics; absent when unknown"`
}

// DataColumn is the name and type of one result column.
type DataColumn struct {
	Name string `json:"name"`
	Type string `json:"type" doc:"The engine's name for the column type"`
}

// DataResultSet is the result of one statement. A null cell is SQL NULL; every
// other value is its text form, clipped when very long (the clipped text ends
// with an ellipsis).
type DataResultSet struct {
	Columns    []DataColumn `json:"columns"`
	Rows       [][]*string  `json:"rows"`
	RowCount   int          `json:"rowCount"`
	Truncated  bool         `json:"truncated,omitempty" doc:"More rows existed than are returned"`
	CommandTag string       `json:"commandTag,omitempty" doc:"The server's completion tag, for example INSERT 0 1"`
}

// DataResult is the answer to a query or a table page.
type DataResult struct {
	Results        []DataResultSet `json:"results"`
	DurationMs     int64           `json:"durationMs"`
	TruncatedCells bool            `json:"truncatedCells,omitempty" doc:"Some cell was longer than the per-cell limit and is clipped"`
}

// DataTables is the answer to the table listing.
type DataTables struct {
	Tables []DataTable `json:"tables"`
}

// BrowseInput selects one page of a table.
type BrowseInput struct {
	Schema string
	Table  string
	Offset int
}

// QueryInput is one SQL batch.
type QueryInput struct {
	SQL         string
	AllowWrites bool
	// Confirm must equal the service slug for a write in a production
	// environment.
	Confirm string
}

// dataTarget is a database the Data tab may talk to.
type dataTarget struct {
	sc       services.Scope
	spec     engines.Spec
	serverID uuid.UUID
	creds    engines.Credentials
}

// dataPreflight resolves the database and checks that the caller may read it
// and that the agent can answer: the same checks for every Data operation.
func (s *Service) dataPreflight(ctx context.Context, id uuid.UUID) (dataTarget, error) {
	sc, spec, eng, err := s.managed(ctx, policy.ScopeDeploymentsWrite, id)
	if err != nil {
		return dataTarget{}, err
	}
	if err := policy.RequireRole(ctx, &sc.ProjectID, auth.RoleOwner, auth.RoleAdmin, auth.RoleDeveloper); err != nil {
		return dataTarget{}, err
	}
	if !DataSupported(eng.ID) {
		return dataTarget{}, apierr.Conflict(CodeDataNotSupported, eng.Label+" has no Data tab; connect with its own client").
			WithDetails(map[string]any{"engine": eng.ID})
	}
	if sc.Service.DesiredState == services.DesiredStopped {
		return dataTarget{}, apierr.Conflict(CodeServiceStopped, "the database is stopped: start it to browse its data")
	}
	srv, err := s.currentServerRow(ctx, s.q, sc)
	if err != nil {
		return dataTarget{}, err
	}
	if s.hasFence(ctx, s.q, sc.Service.ID) {
		return dataTarget{}, apierr.Conflict(CodeDatabaseFenced, sc.Service.Slug+" is a fenced old primary and is not running")
	}
	creds, err := s.credentials(ctx, s.q, sc, spec, eng)
	if err != nil {
		return dataTarget{}, err
	}
	return dataTarget{sc: sc, spec: spec, serverID: srv.ID, creds: creds}, nil
}

// dataRequest sends one db_query to the agent and decodes the answer.
func (s *Service) dataRequest(ctx context.Context, t dataTarget, p agentprotocol.DBQueryPayload) (agentprotocol.DBQueryResult, error) {
	if s.req == nil {
		return agentprotocol.DBQueryResult{}, errors.New("database queries are not configured")
	}
	p.ServiceID = t.sc.Service.ID.String()
	p.Engine = t.spec.Engine
	p.Database = t.creds.Database
	p.TimeoutSeconds = dataTimeoutSeconds
	secrets := map[string]string{agentprotocol.SecretDBUser: t.creds.User, agentprotocol.SecretDBPassword: t.creds.Password}
	res, err := s.req.Request(ctx, t.serverID, agentprotocol.KindDBQuery, p, secrets, dataRequestTimeout)
	if err != nil {
		return agentprotocol.DBQueryResult{}, mapRequestError(err)
	}
	if res.Outcome != agentprotocol.OutcomeSucceeded {
		msg := res.Error
		if msg == "" {
			msg = "the database did not run the request"
		}
		return agentprotocol.DBQueryResult{}, apierr.Unprocessable(CodeQueryFailed, msg)
	}
	var out agentprotocol.DBQueryResult
	if err := json.Unmarshal(res.Data, &out); err != nil {
		return agentprotocol.DBQueryResult{}, fmt.Errorf("decode db_query result: %w", err)
	}
	return out, nil
}

// mapRequestError turns a delivery failure into an API error.
func mapRequestError(err error) error {
	switch {
	case errors.Is(err, context.DeadlineExceeded):
		return apierr.New(http.StatusGatewayTimeout, "server_timeout", "the server did not answer in time")
	case errors.Is(err, context.Canceled):
		return err
	}
	if ae, ok := apierr.As(err); ok {
		return ae
	}
	return apierr.New(http.StatusServiceUnavailable, "server_offline", "the server disconnected before it answered")
}

// ListTables lists the tables and views of the database.
func (s *Service) ListTables(ctx context.Context, serviceID uuid.UUID) (DataTables, error) {
	t, err := s.dataPreflight(ctx, serviceID)
	if err != nil {
		return DataTables{}, err
	}
	out, err := s.dataRequest(ctx, t, agentprotocol.DBQueryPayload{Op: agentprotocol.DBOpTables})
	if err != nil {
		return DataTables{}, err
	}
	res := DataTables{Tables: make([]DataTable, 0, len(out.Tables))}
	for _, tb := range out.Tables {
		res.Tables = append(res.Tables, DataTable{Schema: tb.Schema, Name: tb.Name, Kind: tb.Kind, RowEstimate: tb.RowEstimate})
	}
	return res, nil
}

// BrowseTable returns one page of a table (always read-only). It is audited as
// database.browse.
func (s *Service) BrowseTable(ctx context.Context, serviceID uuid.UUID, in BrowseInput) (DataResult, error) {
	t, err := s.dataPreflight(ctx, serviceID)
	if err != nil {
		return DataResult{}, err
	}
	if in.Offset < 0 {
		return DataResult{}, apierr.Unprocessable("invalid_offset", "offset must not be negative")
	}
	if err := s.auditData(ctx, t, ActionDatabaseBrowse, map[string]any{
		"service": t.sc.Service.Slug, "schema": in.Schema, "table": in.Table,
	}); err != nil {
		return DataResult{}, err
	}
	out, err := s.dataRequest(ctx, t, agentprotocol.DBQueryPayload{
		Op: agentprotocol.DBOpBrowse, Schema: in.Schema, Table: in.Table, Offset: in.Offset, MaxRows: dataBrowseMaxRows,
	})
	if err != nil {
		return DataResult{}, err
	}
	return dataResult(out), nil
}

// Query runs SQL on the database. The session is read-only unless writes are
// allowed; a write on a read replica is refused, and one in a production
// environment needs the service slug as confirmation (and, for a guarded
// token, an approval). It is audited as database.query, without the SQL.
func (s *Service) Query(ctx context.Context, serviceID uuid.UUID, in QueryInput) (DataResult, error) {
	t, err := s.dataPreflight(ctx, serviceID)
	if err != nil {
		return DataResult{}, err
	}
	if strings.TrimSpace(in.SQL) == "" {
		return DataResult{}, apierr.Unprocessable("invalid_sql", "enter a statement to run")
	}
	if in.AllowWrites {
		if err := policy.RequireRole(ctx, &t.sc.ProjectID, auth.RoleOwner, auth.RoleAdmin); err != nil {
			return DataResult{}, err
		}
		if t.spec.Replica != nil {
			return DataResult{}, apierr.Conflict(CodeReplicaReadOnly, "this database is a read replica: run write statements on the primary")
		}
		if t.sc.IsProduction && in.Confirm != t.sc.Service.Slug {
			return DataResult{}, apierr.Conflict(CodeConfirmationRequired,
				"this is a production database: type the service name ("+t.sc.Service.Slug+") to confirm the write").
				WithDetails(map[string]any{"confirm": t.sc.Service.Slug})
		}
		if err := t.sc.Guard(ctx, "query_database_write",
			"Run a write statement on "+t.sc.Service.Slug, true, []string{"Changes data in the production database"}, in.SQL); err != nil {
			return DataResult{}, err
		}
	}
	if err := s.auditData(ctx, t, ActionDatabaseQuery, map[string]any{
		"service": t.sc.Service.Slug, "engine": t.spec.Engine, "production": t.sc.IsProduction,
		"allowWrites": in.AllowWrites, "verb": sqlVerb(in.SQL),
	}); err != nil {
		return DataResult{}, err
	}
	out, err := s.dataRequest(ctx, t, agentprotocol.DBQueryPayload{
		Op: agentprotocol.DBOpSQL, SQL: in.SQL, AllowWrites: in.AllowWrites, MaxRows: dataQueryMaxRows,
	})
	if err != nil {
		return DataResult{}, err
	}
	return dataResult(out), nil
}

// auditData records a Data operation before it reaches the agent; when the
// record cannot be written the operation does not run.
func (s *Service) auditData(ctx context.Context, t dataTarget, action string, meta map[string]any) error {
	err := db.InTx(ctx, s.pool, func(tx pgx.Tx) error {
		return audit.Record(ctx, tx, audit.Entry{
			Action: action, ResourceType: "service", ResourceID: t.sc.Service.ID.String(), ProjectID: &t.sc.ProjectID, Metadata: meta,
		})
	})
	if err != nil {
		return fmt.Errorf("audit %s: %w", action, err)
	}
	return nil
}

// dataResult maps the agent's answer to the API shape.
func dataResult(out agentprotocol.DBQueryResult) DataResult {
	res := DataResult{Results: make([]DataResultSet, 0, len(out.Results)), DurationMs: out.DurationMs, TruncatedCells: out.TruncatedCells}
	for _, set := range out.Results {
		cols := make([]DataColumn, len(set.Columns))
		for i, c := range set.Columns {
			cols[i] = DataColumn{Name: c.Name, Type: c.Type}
		}
		rows := set.Rows
		if rows == nil {
			rows = [][]*string{}
		}
		res.Results = append(res.Results, DataResultSet{
			Columns: cols, Rows: rows, RowCount: set.RowCount, Truncated: set.Truncated, CommandTag: set.CommandTag,
		})
	}
	return res
}

// sqlVerb is the first keyword of a statement batch in upper case (SELECT,
// INSERT, …), skipping leading whitespace, comments and opening parentheses.
// It is the only part of the SQL the audit log keeps.
func sqlVerb(sql string) string {
	s := sql
	for {
		s = strings.TrimLeftFunc(s, func(r rune) bool { return unicode.IsSpace(r) || r == '(' || r == ';' })
		switch {
		case strings.HasPrefix(s, "--"), strings.HasPrefix(s, "#"):
			i := strings.IndexByte(s, '\n')
			if i < 0 {
				return "OTHER"
			}
			s = s[i+1:]
			continue
		case strings.HasPrefix(s, "/*"):
			i := strings.Index(s[2:], "*/")
			if i < 0 {
				return "OTHER"
			}
			s = s[i+4:]
			continue
		}
		break
	}
	end := 0
	for end < len(s) && end < 16 && (s[end] >= 'a' && s[end] <= 'z' || s[end] >= 'A' && s[end] <= 'Z') {
		end++
	}
	if end == 0 {
		return "OTHER"
	}
	return strings.ToUpper(s[:end])
}

// agentsRequester documents that the hub is the production DataRequester.
var _ DataRequester = (*agents.Hub)(nil)
