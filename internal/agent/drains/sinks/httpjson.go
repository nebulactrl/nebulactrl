package sinks

import (
	"bytes"
	"context"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/nebulactrl/nebulactrl/pkg/agentprotocol"
)

type httpJSONSink struct {
	http        *httpSender
	ndjson      bool
	tsField     string
	messageFld  string
	extraFields map[string]string
}

func validateHTTPJSON(c *agentprotocol.HTTPDrainConfig) error {
	if err := validateURL("http", c.URL); err != nil {
		return err
	}
	switch strings.ToUpper(c.Method) {
	case "", http.MethodPost, http.MethodPut:
	default:
		return errors.New("http drain: method must be POST or PUT")
	}
	switch c.Format {
	case "", agentprotocol.HTTPFormatJSONArray, agentprotocol.HTTPFormatNDJSON:
	default:
		return errors.New("http drain: format must be json_array or ndjson")
	}
	return nil
}

func newHTTPJSON(c *agentprotocol.HTTPDrainConfig, secrets map[string]string) (Sink, error) {
	hdr, err := buildHeaders(secrets, true)
	if err != nil {
		return nil, err
	}
	method := strings.ToUpper(c.Method)
	if method == "" {
		method = http.MethodPost
	}
	s := &httpJSONSink{
		// No gzip: many intakes do not accept a compressed body.
		http:        newHTTPSender(method, c.URL, hdr, false, secrets),
		ndjson:      c.Format == agentprotocol.HTTPFormatNDJSON,
		tsField:     c.TimestampField,
		messageFld:  c.MessageField,
		extraFields: c.ExtraFields,
	}
	if s.tsField == "" {
		s.tsField = "timestamp"
	}
	if s.messageFld == "" {
		s.messageFld = "message"
	}
	return s, nil
}

func (h *httpJSONSink) Close() error { return h.http.close() }

func (h *httpJSONSink) Send(ctx context.Context, batch []Entry) error {
	if len(batch) == 0 {
		return nil
	}
	body, ct, err := h.encode(batch)
	if err != nil {
		return err
	}
	return h.http.send(ctx, ct, body)
}

func (h *httpJSONSink) encode(batch []Entry) ([]byte, string, error) {
	var buf bytes.Buffer
	if !h.ndjson {
		buf.WriteByte('[')
	}
	for i, e := range batch {
		rec, err := marshalJSON(h.record(e))
		if err != nil {
			return nil, "", err
		}
		switch {
		case h.ndjson:
			buf.Write(rec)
			buf.WriteByte('\n')
		default:
			if i > 0 {
				buf.WriteByte(',')
			}
			buf.Write(rec)
		}
	}
	if !h.ndjson {
		buf.WriteByte(']')
		return buf.Bytes(), "application/json", nil
	}
	return buf.Bytes(), "application/x-ndjson", nil
}

// record builds one record. Precedence, lowest first: the standard metadata
// fields, the configured extra fields, the timestamp and message fields.
func (h *httpJSONSink) record(e Entry) map[string]any {
	r := make(map[string]any, 16+len(h.extraFields))
	set := func(k, v string) {
		if v != "" {
			r[k] = v
		}
	}
	set("level", normLevel(e.Level))
	set("stream", e.Stream)
	set("service", e.Service)
	set("service_id", e.ServiceID)
	set("project", e.Project)
	set("project_id", e.ProjectID)
	set("environment", e.Environment)
	set("environment_id", e.EnvironmentID)
	set("deployment_id", e.DeploymentID)
	r["replica"] = e.Replica
	set("server", e.Server)
	for k, v := range h.extraFields {
		if k != "" {
			r[k] = v
		}
	}
	r[h.tsField] = e.ts().UTC().Format(time.RFC3339Nano)
	r[h.messageFld] = e.Text
	return r
}
