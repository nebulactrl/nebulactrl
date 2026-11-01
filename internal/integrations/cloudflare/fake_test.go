package cloudflare

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
)

// secretToken is the canary the tests look for in logs, audit rows and responses.
const secretToken = "cf-canary-token-5f3a9c1d"

// fakeCF is a stateful in-memory stand-in for the Cloudflare API v4: zones,
// the zone SSL setting, token verification and DNS records.
type fakeCF struct {
	t   *testing.T
	srv *httptest.Server

	mu    sync.Mutex
	token string
	zones []Zone
	recs  map[string][]*Record // zone id -> records
	ssl   map[string]string    // zone id -> mode (default "full")
	// presented is the bearer token of the request being served.
	presented string
	nextID    int
	// writes logs every mutating request ("POST /zones/z1/dns_records").
	writes []string
	// failWrites makes every record write answer with this status.
	failWrites int
	// noTags makes Cloudflare refuse record tags (plans without tag support).
	noTags bool
	// echoToken puts the bearer token into error messages (to test redaction).
	echoToken bool
	// verifyStatus is the status the token endpoint reports ("active" by default).
	verifyStatus string
	// verifyDenied makes the token endpoint answer 403 (account-owned tokens).
	verifyDenied bool
	// aop is the zone setting tls_client_auth per zone id ("off" by default).
	aop map[string]string
	// aopDenied makes the tls_client_auth setting answer 403 (token without Zone Settings Edit).
	aopDenied bool
	// sslDenied makes the ssl setting answer 403 (token without Zone Settings Read).
	sslDenied bool
}

func newFake(t *testing.T) *fakeCF {
	t.Helper()
	f := &fakeCF{t: t, token: secretToken, recs: map[string][]*Record{}, ssl: map[string]string{}, aop: map[string]string{}, verifyStatus: "active"}
	f.srv = httptest.NewServer(http.HandlerFunc(f.handle))
	t.Cleanup(f.srv.Close)
	return f
}

func (f *fakeCF) client() *Client {
	return New(Config{BaseURL: f.srv.URL + "/client/v4", Token: f.token, HTTPClient: f.srv.Client()})
}

func (f *fakeCF) addZone(id, name string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.zones = append(f.zones, Zone{ID: id, Name: name, Status: "active", AccountID: "acc1", AccountName: "Account acc1"})
	if _, ok := f.recs[id]; !ok {
		f.recs[id] = nil
	}
}

func (f *fakeCF) removeZone(id string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := f.zones[:0]
	for _, z := range f.zones {
		if z.ID != id {
			out = append(out, z)
		}
	}
	f.zones = out
}

// put adds a record directly (a record somebody else created).
func (f *fakeCF) put(r Record) *Record {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.nextID++
	r.ID = "rec" + strconv.Itoa(f.nextID)
	if r.TTL == 0 {
		r.TTL = 1
	}
	cp := r
	f.recs["z1"] = append(f.recs["z1"], &cp)
	return &cp
}

// records returns a copy of the records of one name (all types).
func (f *fakeCF) records(name string) []Record {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []Record
	for _, r := range f.recs["z1"] {
		if strings.EqualFold(r.Name, name) {
			out = append(out, *r)
		}
	}
	return out
}

func (f *fakeCF) writeLog() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.writes...)
}

func (f *fakeCF) resetWrites() {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.writes = nil
}

func (f *fakeCF) setComment(zone, id, comment string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, r := range f.recs[zone] {
		if r.ID == id {
			r.Comment = comment
		}
	}
}

func (f *fakeCF) reply(w http.ResponseWriter, result any, info *ResultInfo) {
	body := map[string]any{"success": true, "errors": []any{}, "messages": []any{}, "result": result}
	if info != nil {
		body["result_info"] = info
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(body)
}

func (f *fakeCF) fail(w http.ResponseWriter, status, code int, msg string) {
	if f.echoToken {
		msg += " (token " + f.presented + ")"
	}
	body := map[string]any{"success": false, "errors": []any{map[string]any{"code": code, "message": msg}}, "messages": []any{}, "result": nil}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}

func (f *fakeCF) handle(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	f.presented = strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
	defer f.mu.Unlock()
	if r.Header.Get("Authorization") != "Bearer "+f.token {
		f.fail(w, http.StatusUnauthorized, 10000, "Authentication error")
		return
	}
	path := strings.TrimPrefix(r.URL.Path, "/client/v4")
	parts := strings.Split(strings.Trim(path, "/"), "/")
	switch {
	case path == "/user/tokens/verify":
		if f.verifyDenied {
			f.fail(w, http.StatusForbidden, 1000, "Invalid API Token")
			return
		}
		f.reply(w, map[string]any{"id": "tok1", "status": f.verifyStatus, "expires_on": "2031-01-02T03:04:05Z"}, nil)
	case len(parts) == 4 && parts[0] == "accounts" && parts[2] == "tokens" && parts[3] == "verify":
		f.reply(w, map[string]any{"id": "tok1", "status": f.verifyStatus}, nil)
	case path == "/zones":
		f.listZones(w, r)
	case len(parts) == 4 && parts[0] == "zones" && parts[2] == "settings" && parts[3] == "ssl":
		if f.sslDenied {
			f.fail(w, http.StatusForbidden, 9109, "Unauthorized to access requested resource")
			return
		}
		mode := f.ssl[parts[1]]
		if mode == "" {
			mode = "full"
		}
		f.reply(w, map[string]any{"id": "ssl", "value": mode}, nil)
	case len(parts) == 4 && parts[0] == "zones" && parts[2] == "settings" && parts[3] == "tls_client_auth":
		f.tlsClientAuth(w, r, parts[1])
	case len(parts) >= 3 && parts[0] == "zones" && parts[2] == "dns_records":
		f.dnsRecords(w, r, parts)
	default:
		f.fail(w, http.StatusNotFound, 7000, "No route for that URI")
	}
}

func (f *fakeCF) listZones(w http.ResponseWriter, r *http.Request) {
	per, _ := strconv.Atoi(r.URL.Query().Get("per_page"))
	page, _ := strconv.Atoi(r.URL.Query().Get("page"))
	if per <= 0 {
		per = 20
	}
	if page <= 0 {
		page = 1
	}
	total := len(f.zones)
	pages := (total + per - 1) / per
	lo, hi := (page-1)*per, page*per
	if lo > total {
		lo = total
	}
	if hi > total {
		hi = total
	}
	out := []map[string]any{}
	for _, z := range f.zones[lo:hi] {
		out = append(out, map[string]any{
			"id": z.ID, "name": z.Name, "status": z.Status, "paused": z.Paused,
			"account": map[string]any{"id": z.AccountID, "name": z.AccountName},
		})
	}
	f.reply(w, out, &ResultInfo{Page: page, PerPage: per, Count: len(out), TotalCount: total, TotalPages: pages})
}

// tlsClientAuth serves the zone setting of global Authenticated Origin Pulls:
// GET reads it, PATCH {"value":"on"|"off"} changes it (Edit zone setting).
func (f *fakeCF) tlsClientAuth(w http.ResponseWriter, r *http.Request, zone string) {
	if !f.hasZone(zone) {
		f.fail(w, http.StatusNotFound, 7003, "Could not route to /zones/"+zone+", perhaps your object identifier is invalid?")
		return
	}
	if f.aopDenied {
		f.fail(w, http.StatusForbidden, 9109, "Unauthorized to access requested resource")
		return
	}
	mode := f.aop[zone]
	if mode == "" {
		mode = "off"
	}
	switch r.Method {
	case http.MethodGet:
		f.reply(w, map[string]any{"id": "tls_client_auth", "value": mode, "editable": true}, nil)
	case http.MethodPatch:
		var body struct {
			Value string `json:"value"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil || (body.Value != "on" && body.Value != "off") {
			f.fail(w, http.StatusBadRequest, 1004, "Invalid value for zone setting tls_client_auth")
			return
		}
		f.writes = append(f.writes, "PATCH /zones/"+zone+"/settings/tls_client_auth "+body.Value)
		f.aop[zone] = body.Value
		f.reply(w, map[string]any{"id": "tls_client_auth", "value": body.Value, "editable": true}, nil)
	default:
		f.fail(w, http.StatusMethodNotAllowed, 1005, "Method not allowed")
	}
}

func (f *fakeCF) hasZone(id string) bool {
	for _, z := range f.zones {
		if z.ID == id {
			return true
		}
	}
	return false
}

func (f *fakeCF) dnsRecords(w http.ResponseWriter, r *http.Request, parts []string) {
	zone := parts[1]
	if !f.hasZone(zone) {
		f.fail(w, http.StatusNotFound, 7003, "Could not route to /zones/"+zone+", perhaps your object identifier is invalid?")
		return
	}
	if r.Method != http.MethodGet {
		f.writes = append(f.writes, r.Method+" "+strings.TrimPrefix(r.URL.Path, "/client/v4"))
		if f.failWrites != 0 {
			f.fail(w, f.failWrites, 9999, "simulated failure")
			return
		}
	}
	if len(parts) == 3 {
		switch r.Method {
		case http.MethodGet:
			name := r.URL.Query().Get("name.exact")
			out := []*Record{}
			for _, rec := range f.recs[zone] {
				if name == "" || strings.EqualFold(rec.Name, name) {
					out = append(out, rec)
				}
			}
			f.reply(w, out, &ResultInfo{Page: 1, PerPage: 100, Count: len(out), TotalCount: len(out), TotalPages: 1})
		case http.MethodPost:
			var in RecordInput
			if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
				f.fail(w, http.StatusBadRequest, 9005, "bad body")
				return
			}
			if f.noTags && len(in.Tags) > 0 {
				f.fail(w, http.StatusBadRequest, 9007, "Tags are not available on your plan")
				return
			}
			for _, rec := range f.recs[zone] {
				if strings.EqualFold(rec.Name, in.Name) && (in.Type == "CNAME" || rec.Type == "CNAME" || (rec.Type == in.Type && rec.Content == in.Content)) {
					f.fail(w, http.StatusBadRequest, 81053, "An A, AAAA, or CNAME record with that host already exists.")
					return
				}
			}
			f.nextID++
			rec := &Record{ID: "rec" + strconv.Itoa(f.nextID), Type: in.Type, Name: in.Name, Content: in.Content, Proxied: in.Proxied, TTL: in.TTL, Comment: in.Comment, Tags: in.Tags}
			f.recs[zone] = append(f.recs[zone], rec)
			f.reply(w, rec, nil)
		default:
			f.fail(w, http.StatusMethodNotAllowed, 7001, "method not allowed")
		}
		return
	}
	id := parts[3]
	var found *Record
	idx := -1
	for i, rec := range f.recs[zone] {
		if rec.ID == id {
			found, idx = rec, i
		}
	}
	if found == nil {
		f.fail(w, http.StatusNotFound, 81044, "Record does not exist.")
		return
	}
	switch r.Method {
	case http.MethodGet:
		f.reply(w, found, nil)
	case http.MethodPatch:
		var in RecordInput
		if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
			f.fail(w, http.StatusBadRequest, 9005, "bad body")
			return
		}
		if f.noTags && len(in.Tags) > 0 {
			f.fail(w, http.StatusBadRequest, 9007, "Tags are not available on your plan")
			return
		}
		if in.Type != "" {
			found.Type = in.Type
		}
		found.Content, found.Proxied = in.Content, in.Proxied
		if in.TTL != 0 {
			found.TTL = in.TTL
		}
		if in.Comment != "" {
			found.Comment = in.Comment
		}
		if len(in.Tags) > 0 {
			found.Tags = in.Tags
		}
		f.reply(w, found, nil)
	case http.MethodDelete:
		f.recs[zone] = append(f.recs[zone][:idx], f.recs[zone][idx+1:]...)
		f.reply(w, map[string]any{"id": id}, nil)
	default:
		f.fail(w, http.StatusMethodNotAllowed, 7001, fmt.Sprintf("method %s not allowed", r.Method))
	}
}
