package sinks

import (
	"context"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/nebulactrl/nebulactrl/pkg/agentprotocol"
)

// OTLP/HTTP logs in the canonical protobuf-JSON encoding.

type otlpValue struct {
	StringValue *string `json:"stringValue,omitempty"`
	IntValue    *string `json:"intValue,omitempty"` // int64 is a string in proto-JSON
}

type otlpKV struct {
	Key   string    `json:"key"`
	Value otlpValue `json:"value"`
}

type otlpRecord struct {
	TimeUnixNano         string    `json:"timeUnixNano"`
	ObservedTimeUnixNano string    `json:"observedTimeUnixNano"`
	SeverityNumber       int       `json:"severityNumber"`
	SeverityText         string    `json:"severityText,omitempty"`
	Body                 otlpValue `json:"body"`
	Attributes           []otlpKV  `json:"attributes,omitempty"`
}

type otlpScope struct {
	Name string `json:"name"`
}

type otlpScopeLogs struct {
	Scope      otlpScope    `json:"scope"`
	LogRecords []otlpRecord `json:"logRecords"`
}

type otlpResource struct {
	Attributes []otlpKV `json:"attributes"`
}

type otlpResourceLogs struct {
	Resource  otlpResource    `json:"resource"`
	ScopeLogs []otlpScopeLogs `json:"scopeLogs"`
}

type otlpRequest struct {
	ResourceLogs []otlpResourceLogs `json:"resourceLogs"`
}

type otlpSink struct {
	http  *httpSender
	extra []otlpKV // configured resource attributes, sorted by key
	now   func() time.Time
}

func newOTLP(c *agentprotocol.OTLPDrainConfig, secrets map[string]string) (Sink, error) {
	hdr, err := buildHeaders(secrets, false)
	if err != nil {
		return nil, err
	}
	keys := make([]string, 0, len(c.ResourceAttributes))
	for k := range c.ResourceAttributes {
		if k != "" {
			keys = append(keys, k)
		}
	}
	sort.Strings(keys)
	extra := make([]otlpKV, len(keys))
	for i, k := range keys {
		extra[i] = strKV(k, c.ResourceAttributes[k])
	}
	return &otlpSink{http: newHTTPSender(http.MethodPost, c.URL, hdr, true, secrets), extra: extra, now: time.Now}, nil
}

func strKV(k, v string) otlpKV { return otlpKV{Key: k, Value: otlpValue{StringValue: &v}} }

func intKV(k string, n int) otlpKV {
	s := strconv.Itoa(n)
	return otlpKV{Key: k, Value: otlpValue{IntValue: &s}}
}

func (o *otlpSink) Send(ctx context.Context, batch []Entry) error {
	if len(batch) == 0 {
		return nil
	}
	body, err := o.encode(batch, o.now())
	if err != nil {
		return err
	}
	// A 2xx with partialSuccess.rejectedLogRecords > 0 is still a success: the
	// batch was delivered and resending it cannot change the outcome.
	return o.http.send(ctx, "application/json", body)
}

func (o *otlpSink) Close() error { return o.http.close() }

var otlpSeverityNumber = map[string]int{"debug": 5, "info": 9, "warn": 13, "error": 17}

func (o *otlpSink) encode(batch []Entry, observed time.Time) ([]byte, error) {
	observedNs := strconv.FormatInt(observed.UnixNano(), 10)
	var req otlpRequest
	index := map[string]int{} // resource key -> position in req.ResourceLogs
	for _, e := range batch {
		key := strings.Join([]string{e.ServiceID, e.Service, e.ProjectID, e.Project, e.EnvironmentID, e.Environment, e.Server}, "\x00")
		i, ok := index[key]
		if !ok {
			i = len(req.ResourceLogs)
			index[key] = i
			req.ResourceLogs = append(req.ResourceLogs, otlpResourceLogs{
				Resource:  otlpResource{Attributes: o.resourceAttrs(e)},
				ScopeLogs: []otlpScopeLogs{{Scope: otlpScope{Name: "nebulactrl"}}},
			})
		}
		level := normLevel(e.Level)
		rec := otlpRecord{
			TimeUnixNano:         strconv.FormatInt(e.ts().UnixNano(), 10),
			ObservedTimeUnixNano: observedNs,
			SeverityNumber:       otlpSeverityNumber[level],
			Body:                 otlpValue{StringValue: &e.Text},
		}
		if level != "" {
			rec.SeverityText = strings.ToUpper(level)
		}
		if e.DeploymentID != "" {
			rec.Attributes = append(rec.Attributes, strKV("nebulactrl.deployment.id", e.DeploymentID))
		}
		rec.Attributes = append(rec.Attributes, intKV("nebulactrl.replica", e.Replica))
		if e.Stream != "" {
			rec.Attributes = append(rec.Attributes, strKV("log.iostream", e.Stream))
		}
		sl := &req.ResourceLogs[i].ScopeLogs[0]
		sl.LogRecords = append(sl.LogRecords, rec)
	}
	return marshalJSON(req)
}

// resourceAttrs lists the resource attributes of a service; configured
// attributes never replace the built-in ones.
func (o *otlpSink) resourceAttrs(e Entry) []otlpKV {
	attrs := make([]otlpKV, 0, 7+len(o.extra))
	seen := map[string]bool{}
	add := func(k, v string) {
		if v != "" {
			attrs = append(attrs, strKV(k, v))
			seen[k] = true
		}
	}
	add("service.name", e.Service)
	add("service.namespace", e.Project)
	add("deployment.environment.name", e.Environment)
	add("host.name", e.Server)
	add("nebulactrl.service.id", e.ServiceID)
	add("nebulactrl.project.id", e.ProjectID)
	add("nebulactrl.environment.id", e.EnvironmentID)
	for _, kv := range o.extra {
		if !seen[kv.Key] && *kv.Value.StringValue != "" {
			attrs = append(attrs, kv)
		}
	}
	return attrs
}
