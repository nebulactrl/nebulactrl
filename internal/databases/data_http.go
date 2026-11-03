package databases

import (
	"context"
	"net/http"

	"github.com/danielgtaylor/huma/v2"
	"github.com/google/uuid"

	"github.com/nebulactrl/nebulactrl/internal/api"
	"github.com/nebulactrl/nebulactrl/internal/policy"
)

type dataTablesOutput struct {
	CacheControl string `header:"Cache-Control"`
	Body         DataTables
}

type browseTableInput struct {
	ID   uuid.UUID `path:"serviceId"`
	Body struct {
		Schema string `json:"schema,omitempty" maxLength:"256" doc:"Schema of the table (PostgreSQL schema, MySQL/MariaDB/ClickHouse database), as listed by the tables operation"`
		Table  string `json:"table" minLength:"1" maxLength:"256"`
		Offset int    `json:"offset" minimum:"0" maximum:"1000000000" doc:"Rows to skip; pages are 100 rows"`
	}
}

type queryDatabaseInput struct {
	ID   uuid.UUID `path:"serviceId"`
	Body struct {
		SQL         string `json:"sql" minLength:"1" maxLength:"65536" doc:"One or more statements"`
		AllowWrites bool   `json:"allowWrites,omitempty" doc:"Lift the read-only session so statements may change data. Off by default."`
		Confirm     string `json:"confirm,omitempty" doc:"Required with allowWrites in a production environment: the service slug"`
	}
}

type dataResultOutput struct {
	CacheControl string `header:"Cache-Control"`
	Body         DataResult
}

const noStore = "no-store, max-age=0"

func registerData(h huma.API, svc *Service) {
	errs := func(extra ...int) []int {
		return append([]int{http.StatusUnauthorized, http.StatusForbidden}, extra...)
	}
	nf, uc, cf := http.StatusNotFound, http.StatusUnprocessableEntity, http.StatusConflict
	// 503 and 504: the server is offline or did not answer.
	off, slow := http.StatusServiceUnavailable, http.StatusGatewayTimeout

	huma.Register(h, api.WithScopes(huma.Operation{
		OperationID: "listDatabaseTables", Method: http.MethodGet, Path: "/services/{serviceId}/database/tables", Tags: []string{"databases"},
		Summary: "List the tables of a SQL database",
		Description: "Lists the tables and views of the database through its agent (PostgreSQL, MySQL, MariaDB and ClickHouse). Needs `deployments:write` " +
			"and the owner, admin or developer role. 409 codes: `data_not_supported`, `service_stopped`, `not_deployed`, `database_fenced`.",
		Errors: errs(nf, cf, uc, off, slow),
	}, policy.ScopeDeploymentsWrite), func(ctx context.Context, in *serviceIDInput) (*dataTablesOutput, error) {
		r, err := svc.ListTables(ctx, in.ID)
		if err != nil {
			return nil, err
		}
		return &dataTablesOutput{CacheControl: noStore, Body: r}, nil
	})

	huma.Register(h, api.WithScopes(huma.Operation{
		OperationID: "browseDatabaseTable", Method: http.MethodPost, Path: "/services/{serviceId}/database/rows", Tags: []string{"databases"},
		Summary: "Read a page of a table (audited)",
		Description: "Returns 100 rows of one table from `offset`, read-only, and records `database.browse` in the audit log. Cells longer than 4 KiB are clipped. " +
			"Same preconditions and 409 codes as the table listing; 422 `query_failed` carries the database's error.",
		Errors: errs(nf, cf, uc, off, slow),
	}, policy.ScopeDeploymentsWrite), func(ctx context.Context, in *browseTableInput) (*dataResultOutput, error) {
		r, err := svc.BrowseTable(ctx, in.ID, BrowseInput{Schema: in.Body.Schema, Table: in.Body.Table, Offset: in.Body.Offset})
		if err != nil {
			return nil, err
		}
		return &dataResultOutput{CacheControl: noStore, Body: r}, nil
	})

	huma.Register(h, api.WithScopes(huma.Operation{
		OperationID: "queryDatabase", Method: http.MethodPost, Path: "/services/{serviceId}/database/query", Tags: []string{"databases"},
		Summary: "Run SQL on a database (audited)",
		Description: "Runs one or more statements through the database's agent with a 30 second timeout and returns up to 1000 rows per result set " +
			"(at most 10 result sets, cells clipped at 4 KiB). The session is read-only unless `allowWrites` is set, which needs the owner or admin role (403 otherwise); writes are refused on a read replica " +
			"(409 `replica_read_only`) and in a production environment need `confirm` to equal the service slug (409 `confirmation_required`) " +
			"and, for a guarded token, an approval. Recorded in the audit log as `database.query` with the first SQL keyword, never the statement. " +
			"422 `query_failed` carries the database's error. Same preconditions and 409 codes as the table listing.",
		Errors: errs(nf, cf, uc, off, slow),
	}, policy.ScopeDeploymentsWrite), func(ctx context.Context, in *queryDatabaseInput) (*dataResultOutput, error) {
		r, err := svc.Query(ctx, in.ID, QueryInput{SQL: in.Body.SQL, AllowWrites: in.Body.AllowWrites, Confirm: in.Body.Confirm})
		if err != nil {
			return nil, err
		}
		return &dataResultOutput{CacheControl: noStore, Body: r}, nil
	})
}
