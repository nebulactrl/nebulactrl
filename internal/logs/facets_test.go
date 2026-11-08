package logs

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/nebulactrl/nebulactrl/internal/apierr"
	"github.com/nebulactrl/nebulactrl/internal/policy"
	"github.com/nebulactrl/nebulactrl/pkg/agentprotocol"
)

func facetField(key string, count int64, values ...agentprotocol.FacetValue) agentprotocol.FacetField {
	return agentprotocol.FacetField{Key: key, Count: count, Values: values}
}

func fv(value string, count int64) agentprotocol.FacetValue {
	return agentprotocol.FacetValue{Value: value, Count: count}
}

func fieldOf(t *testing.T, fields []agentprotocol.FacetField, key string) agentprotocol.FacetField {
	t.Helper()
	for _, f := range fields {
		if f.Key == key {
			return f
		}
	}
	require.FailNow(t, "no such field", key)
	return agentprotocol.FacetField{}
}

// valuesOf renders the values of a field as "value=count", in order.
func valuesOf(f agentprotocol.FacetField) []string {
	out := make([]string, len(f.Values))
	for i, v := range f.Values {
		out[i] = fmt.Sprintf("%s=%d", v.Value, v.Count)
	}
	return out
}

func keysOf(fields []agentprotocol.FacetField) []string {
	out := make([]string, len(fields))
	for i, f := range fields {
		out[i] = f.Key
	}
	return out
}

func TestMergeFacetsSumsCountsMinMaxHistogramAndFlags(t *testing.T) {
	t0 := time.Date(2026, 11, 1, 12, 0, 0, 0, time.UTC)
	mask := func(s string) string { return strings.ReplaceAll(s, "hunter2-secret", "••••••") }
	a := facetPart{service: "web", source: agentprotocol.LogSourceRuntime, redact: mask, res: agentprotocol.LogsFacetsResult{
		Matched: 10, Scanned: 100, Partial: true, PartialFrom: new(t0.Add(time.Minute)), QueryErrors: []string{"bad"}, Capturing: true,
		Facets: []agentprotocol.FacetField{
			facetField("level", 10, fv("error", 4), fv("info", 6)),
			facetField("stream", 10, fv("runtime", 10)),
		},
		Attrs: []agentprotocol.FacetField{
			{Key: "@duration_ms", Count: 10, Numeric: true, Min: new(float64(5)), Max: new(float64(900)), Values: []agentprotocol.FacetValue{fv("5", 1), fv("900", 1)}},
			facetField("@token", 3, fv("hunter2-secret", 3)),
			{Key: "@size", Count: 2, Numeric: true, Min: new(float64(1)), Max: new(float64(2)), Values: []agentprotocol.FacetValue{fv("1", 1), fv("2", 1)}},
		},
		Histogram: []agentprotocol.HistogramBucket{{Start: t0, Count: 5, Errors: 1}, {Start: t0.Add(time.Minute), Count: 5}},
	}}
	b := facetPart{service: "api", source: agentprotocol.LogSourceRequests, redact: mask, res: agentprotocol.LogsFacetsResult{
		Matched: 4, Scanned: 40, Partial: true, PartialFrom: new(t0.Add(2 * time.Minute)), QueryErrors: []string{"bad", "worse"}, Capturing: true,
		Facets: []agentprotocol.FacetField{
			facetField("level", 4, fv("info", 3), fv("warn", 1)),
			facetField("stream", 4, fv("requests", 4)),
		},
		Attrs: []agentprotocol.FacetField{
			{Key: "@duration_ms", Count: 4, Numeric: true, Min: new(float64(2)), Max: new(float64(400)), Values: []agentprotocol.FacetValue{fv("2", 2), fv("400", 2)}},
			facetField("@token", 1, fv("hunter2-secret", 1)),
			facetField("@size", 1, fv("big", 1)), // not numeric here: the merged key is not numeric
			facetField("@method", 4, fv("GET", 3), fv("POST", 1)),
		},
		Histogram: []agentprotocol.HistogramBucket{{Start: t0, Count: 1, Errors: 1}, {Start: t0.Add(time.Minute), Count: 3, Errors: 2}},
	}}
	page := mergeFacets([]facetPart{a, b}, false)

	require.Equal(t, int64(14), page.Matched)
	require.Equal(t, int64(140), page.Scanned)
	require.True(t, page.Partial)
	require.Equal(t, t0.Add(2*time.Minute), *page.PartialFrom, "the latest start is the range every count covers")
	require.Equal(t, []string{"bad", "worse"}, page.QueryErrors)
	require.True(t, page.Capturing)
	require.True(t, page.RequestsCapturing)

	// Counts are summed per key and value.
	require.Equal(t, []string{"level", "stream"}, keysOf(page.Facets), "no service facet on a service")
	level := fieldOf(t, page.Facets, "level")
	require.Equal(t, int64(14), level.Count)
	require.Equal(t, []string{"info=9", "error=4", "warn=1"}, valuesOf(level), "most frequent first")
	require.Equal(t, []string{"runtime=10", "requests=4"}, valuesOf(fieldOf(t, page.Facets, "stream")))

	// Attributes: most frequent key first; min and max merged; numeric only when every answer is numeric.
	require.Equal(t, []string{"@duration_ms", "@method", "@token", "@size"}, keysOf(page.Attrs))
	dur := fieldOf(t, page.Attrs, "@duration_ms")
	require.True(t, dur.Numeric)
	require.Equal(t, float64(2), *dur.Min)
	require.Equal(t, float64(900), *dur.Max)
	require.Equal(t, int64(14), dur.Count)
	size := fieldOf(t, page.Attrs, "@size")
	require.False(t, size.Numeric)
	require.Equal(t, float64(1), *size.Min)
	require.Equal(t, float64(2), *size.Max)

	// A secret value is masked in every answer before the counts are added.
	token := fieldOf(t, page.Attrs, "@token")
	require.Equal(t, []string{"••••••=4"}, valuesOf(token))
	raw, _ := json.Marshal(page)
	require.NotContains(t, string(raw), "hunter2-secret")

	// Histograms add up bucket by bucket.
	require.Equal(t, []agentprotocol.HistogramBucket{{Start: t0, Count: 6, Errors: 2}, {Start: t0.Add(time.Minute), Count: 8, Errors: 2}}, page.Histogram)
}

func TestMergeFacetsKeepsTheTopValuesOnly(t *testing.T) {
	var many []agentprotocol.FacetValue
	for i := range 25 {
		many = append(many, fv(fmt.Sprintf("v%02d", i), 10))
	}
	a := facetPart{res: agentprotocol.LogsFacetsResult{Matched: 250, Facets: []agentprotocol.FacetField{facetField("replica", 250, many...)}}}
	b := facetPart{res: agentprotocol.LogsFacetsResult{Matched: 5, Facets: []agentprotocol.FacetField{facetField("replica", 5, fv("v24", 5))}}}
	page := mergeFacets([]facetPart{a, b}, false)

	replica := fieldOf(t, page.Facets, "replica")
	require.Len(t, replica.Values, agentprotocol.LogFacetsMaxValues)
	require.Equal(t, "v24=15", valuesOf(replica)[0])
	require.Equal(t, "v00=10", valuesOf(replica)[1], "ties by value")
	require.Equal(t, "v18=10", valuesOf(replica)[agentprotocol.LogFacetsMaxValues-1])
	require.Equal(t, int64(255), replica.Count, "the count is of lines, not of the values kept")

	// Attributes are cut to the most frequent ones.
	var attrs []agentprotocol.FacetField
	for i := range agentprotocol.LogFacetsMaxAttrs + 5 {
		attrs = append(attrs, facetField(fmt.Sprintf("@a%02d", i), int64(100-i), fv("x", 1)))
	}
	page = mergeFacets([]facetPart{{res: agentprotocol.LogsFacetsResult{Attrs: attrs}}}, false)
	require.Len(t, page.Attrs, agentprotocol.LogFacetsMaxAttrs)
	require.Equal(t, "@a00", page.Attrs[0].Key)
}

func TestMergeFacetsAddsTheServiceFacetForAnEnvironment(t *testing.T) {
	part := func(service, source string, matched int64) facetPart {
		return facetPart{service: service, source: source, res: agentprotocol.LogsFacetsResult{
			Matched: matched, Capturing: true, Facets: []agentprotocol.FacetField{facetField("level", matched, fv("info", matched))},
		}}
	}
	page := mergeFacets([]facetPart{
		part("web", agentprotocol.LogSourceRuntime, 3),
		part("web", agentprotocol.LogSourceRequests, 4),
		part("api", agentprotocol.LogSourceRuntime, 5),
		part("idle", agentprotocol.LogSourceRuntime, 0),
	}, true)
	require.Equal(t, "service", page.Facets[0].Key, "first")
	svc := fieldOf(t, page.Facets, "service")
	require.Equal(t, int64(12), svc.Count)
	require.Equal(t, []string{"web=7", "api=5"}, valuesOf(svc), "services without a match are not listed")

	// Nothing to ask: nothing that could be off.
	empty := mergeFacets(nil, true)
	require.True(t, empty.Capturing)
	require.True(t, empty.RequestsCapturing)
	require.NotNil(t, empty.Facets)
	require.NotNil(t, empty.Attrs)
	require.NotNil(t, empty.Histogram)

	// Request capture is read from the requests answers only.
	runtimeOnly := mergeFacets([]facetPart{part("web", agentprotocol.LogSourceRuntime, 1)}, false)
	require.True(t, runtimeOnly.Capturing)
	require.False(t, runtimeOnly.RequestsCapturing)
}

func TestMergeFacetsDropsARangeThatIsASecret(t *testing.T) {
	mask := func(s string) string { return strings.ReplaceAll(s, "1234567", "••••••") }
	secret := agentprotocol.FacetField{Key: "@pin", Count: 3, Numeric: true, Min: new(float64(42)), Max: new(float64(1234567)), Values: []agentprotocol.FacetValue{fv("1234567", 1), fv("42", 2)}}
	plain := agentprotocol.FacetField{Key: "@size", Count: 3, Numeric: true, Min: new(float64(1)), Max: new(float64(9)), Values: []agentprotocol.FacetValue{fv("1", 3)}}
	fractional := agentprotocol.FacetField{Key: "@ratio", Count: 1, Numeric: true, Min: new(1234567.5), Max: new(1234567.5), Values: []agentprotocol.FacetValue{fv("1234567.5", 1)}}
	page := mergeFacets([]facetPart{{service: "web", source: agentprotocol.LogSourceRequests, redact: mask, res: agentprotocol.LogsFacetsResult{
		Capturing: true, Attrs: []agentprotocol.FacetField{secret, plain, fractional},
	}}}, false)

	for _, key := range []string{"@pin", "@ratio"} {
		f := fieldOf(t, page.Attrs, key)
		require.False(t, f.Numeric, key)
		require.Nil(t, f.Min, key)
		require.Nil(t, f.Max, key)
	}
	require.Equal(t, []string{"42=2", "••••••=1"}, valuesOf(fieldOf(t, page.Attrs, "@pin")), "the values are masked as before")
	size := fieldOf(t, page.Attrs, "@size")
	require.True(t, size.Numeric)
	require.Equal(t, float64(1), *size.Min)
	require.Equal(t, float64(9), *size.Max)
	raw, _ := json.Marshal(page)
	require.NotContains(t, string(raw), "1234567")
}

func TestMergeFacetsDropsAnAttributeWhoseKeyIsASecret(t *testing.T) {
	mask := func(s string) string { return strings.ReplaceAll(s, "hunter2-secret", "••••••") }
	keyed := facetField("@hunter2-secret", 2, fv("a", 2))
	plain := facetField("@user", 2, fv("bob", 2))
	page := mergeFacets([]facetPart{{service: "web", source: agentprotocol.LogSourceRequests, redact: mask, res: agentprotocol.LogsFacetsResult{
		Capturing: true, Attrs: []agentprotocol.FacetField{keyed, plain},
	}}}, false)

	require.Equal(t, []string{"@user"}, keysOf(page.Attrs))
	raw, _ := json.Marshal(page)
	require.NotContains(t, string(raw), "hunter2-secret")
	require.NotContains(t, string(raw), "••••••")
}

func TestMergeFacetsRequestsCapturingNeedsARealAgentAnswer(t *testing.T) {
	part := func(source string, capturing, never bool) facetPart {
		return facetPart{source: source, never: never, res: agentprotocol.LogsFacetsResult{Capturing: capturing}}
	}
	// A never-deployed service answers "capturing" but no agent said so.
	page := mergeFacets([]facetPart{part(agentprotocol.LogSourceRequests, true, true)}, false)
	require.True(t, page.Capturing)
	require.False(t, page.RequestsCapturing)
	// A real answer of any service counts.
	page = mergeFacets([]facetPart{part(agentprotocol.LogSourceRequests, true, true), part(agentprotocol.LogSourceRequests, true, false)}, false)
	require.True(t, page.RequestsCapturing)
	// A real answer that says off.
	page = mergeFacets([]facetPart{part(agentprotocol.LogSourceRequests, false, false)}, false)
	require.False(t, page.RequestsCapturing)
}

func facetsFrame(res agentprotocol.LogsFacetsResult) agentprotocol.ResultFrame {
	raw, _ := json.Marshal(res)
	return agentprotocol.ResultFrame{Outcome: agentprotocol.OutcomeSucceeded, Data: raw}
}

func TestFacetEndpointsAskEveryServiceAndSourceAndMerge(t *testing.T) {
	f := newFixture(t)
	do := f.serve()
	o := principalOf(f.ctx)
	t0 := time.Date(2026, 11, 1, 12, 0, 0, 0, time.UTC)
	f.agents.online[f.srv1.ID] = true
	f.agents.online[f.srv2.ID] = true
	answer := func(matched int64) func(string, json.RawMessage, map[string]string) agentprotocol.ResultFrame {
		return func(kind string, payload json.RawMessage, _ map[string]string) agentprotocol.ResultFrame {
			require.Equal(t, agentprotocol.KindLogsFacets, kind)
			var q agentprotocol.LogsFacetsQuery
			require.NoError(t, json.Unmarshal(payload, &q))
			return facetsFrame(agentprotocol.LogsFacetsResult{
				Matched: matched, Scanned: matched * 2, Capturing: true,
				Facets:    []agentprotocol.FacetField{facetField("stream", matched, fv(q.Source, matched))},
				Attrs:     []agentprotocol.FacetField{facetField("@token", matched, fv("hunter2-secret", matched))},
				Histogram: []agentprotocol.HistogramBucket{{Start: t0, Count: matched}},
			})
		}
	}
	f.agents.handlers[f.srv1.ID] = answer(3) // web
	f.agents.handlers[f.srv2.ID] = answer(5) // api
	env := "/environments/" + f.envA.ID.String() + "/logs/facets"
	svc := "/services/" + f.web.ID.String() + "/logs/facets"
	from, to := t0, t0.Add(time.Hour)
	qs := url.Values{"from": {from.Format(time.RFC3339)}, "to": {to.Format(time.RFC3339)}, "q": {"level:error"}, "buckets": {"30"}, "sources": {"requests,runtime"}}.Encode()

	code, _, raw := do(o, http.MethodGet, env+"?"+qs, nil)
	require.Equal(t, http.StatusOK, code, string(raw))
	page := asMap(t, raw)
	require.EqualValues(t, 16, page["matched"], "two services, two sources")
	require.EqualValues(t, 32, page["scanned"])
	require.Equal(t, true, page["capturing"])
	require.NotContains(t, string(raw), "hunter2-secret", "attribute values are redacted")
	facets := page["facets"].([]any)
	require.Equal(t, "service", facets[0].(map[string]any)["key"])
	values := facets[0].(map[string]any)["values"].([]any)
	require.Equal(t, "api", values[0].(map[string]any)["value"])
	require.EqualValues(t, 10, values[0].(map[string]any)["count"])
	require.Equal(t, "web", values[1].(map[string]any)["value"])
	require.Len(t, page["histogram"], 1)
	require.EqualValues(t, 16, page["histogram"].([]any)[0].(map[string]any)["count"])

	// Every pair got the same range, query and buckets.
	var asked []agentprotocol.LogsFacetsQuery
	for _, a := range f.agents.asked {
		var q agentprotocol.LogsFacetsQuery
		require.NoError(t, json.Unmarshal(a.payload, &q))
		asked = append(asked, q)
	}
	require.Len(t, asked, 4)
	seen := map[string]bool{}
	for _, q := range asked {
		require.True(t, q.From.Equal(from))
		require.True(t, q.To.Equal(to))
		require.Equal(t, "level:error", q.Query)
		require.Equal(t, 30, q.Buckets)
		seen[q.ServiceName+"/"+q.Source] = true
	}
	require.Equal(t, map[string]bool{"web/runtime": true, "web/requests": true, "api/runtime": true, "api/requests": true}, seen)

	// One service, its requests only; the service has no service facet.
	f.agents.asked = nil
	code, _, raw = do(o, http.MethodGet, svc+"?sources=requests", nil)
	require.Equal(t, http.StatusOK, code, string(raw))
	require.Len(t, f.agents.asked, 1)
	var q agentprotocol.LogsFacetsQuery
	require.NoError(t, json.Unmarshal(f.agents.asked[0].payload, &q))
	require.Equal(t, agentprotocol.LogSourceRequests, q.Source)
	require.Equal(t, f.web.ID.String(), q.ServiceID)
	require.WithinDuration(t, q.To.Add(-15*time.Minute), q.From, time.Second, "the last 15 minutes by default")
	require.WithinDuration(t, time.Now(), q.To, time.Minute)
	page = asMap(t, raw)
	require.EqualValues(t, 3, page["matched"])
	require.Equal(t, "stream", page["facets"].([]any)[0].(map[string]any)["key"])
	require.Equal(t, true, page["requestsCapturing"])

	// An offline server: the environment lists its service, the service endpoint fails.
	f.agents.online[f.srv2.ID] = false
	code, _, raw = do(o, http.MethodGet, env, nil)
	require.Equal(t, http.StatusOK, code, string(raw))
	page = asMap(t, raw)
	require.EqualValues(t, 3, page["matched"])
	unavailable := page["unavailable"].([]any)
	require.Len(t, unavailable, 1)
	require.Equal(t, "api", unavailable[0].(map[string]any)["serviceName"])
	require.Equal(t, "server_offline", unavailable[0].(map[string]any)["reason"])
	code, _, raw = do(o, http.MethodGet, "/services/"+f.api.ID.String()+"/logs/facets", nil)
	require.Equal(t, http.StatusServiceUnavailable, code, string(raw))
	require.Equal(t, "server_offline", asMap(t, raw)["code"])

	// What the agent answers is mapped to the right errors.
	f.agents.handlers[f.srv1.ID] = func(string, json.RawMessage, map[string]string) agentprotocol.ResultFrame {
		return agentprotocol.ResultFrame{Outcome: agentprotocol.OutcomeFailed, Error: "unsupported command"}
	}
	code, _, raw = do(o, http.MethodGet, svc, nil)
	require.Equal(t, http.StatusConflict, code, string(raw))
	require.Equal(t, "agent_outdated", asMap(t, raw)["code"])
}

func TestFacetEndpointsValidateAndAuthorize(t *testing.T) {
	f := newFixture(t)
	do := f.serve()
	o := principalOf(f.ctx)
	t0 := time.Date(2026, 11, 1, 12, 0, 0, 0, time.UTC)
	f.agents.online[f.srv1.ID] = true
	f.agents.handlers[f.srv1.ID] = func(string, json.RawMessage, map[string]string) agentprotocol.ResultFrame {
		return facetsFrame(agentprotocol.LogsFacetsResult{Capturing: true})
	}
	for _, base := range []string{"/services/" + f.web.ID.String() + "/logs/facets", "/environments/" + f.envA.ID.String() + "/logs/facets"} {
		for name, qs := range map[string]string{
			"backwards":    url.Values{"from": {t0.Add(time.Hour).Format(time.RFC3339)}, "to": {t0.Format(time.RFC3339)}}.Encode(),
			"too wide":     url.Values{"from": {t0.Format(time.RFC3339)}, "to": {t0.Add(91 * 24 * time.Hour).Format(time.RFC3339)}}.Encode(),
			"too long":     url.Values{"q": {strings.Repeat("a", 2001)}}.Encode(),
			"bad source":   "sources=runtime,metrics",
			"empty source": "sources=runtime,",
			"buckets":      "buckets=1000",
		} {
			code, _, raw := do(o, http.MethodGet, base+"?"+qs, nil)
			require.Equal(t, http.StatusUnprocessableEntity, code, "%s %s: %s", base, name, raw)
		}
		code, _, raw := do(o, http.MethodGet, base+"?"+url.Values{"from": {t0.Format(time.RFC3339)}, "to": {t0.Add(90 * 24 * time.Hour).Format(time.RFC3339)}}.Encode(), nil)
		require.Equal(t, http.StatusOK, code, "the longest range is the retention window: %s", raw)

		ro := *o
		ro.Scopes = []string{policy.ScopeProjectsRead}
		code, _, _ = do(&ro, http.MethodGet, base, nil)
		require.Equal(t, http.StatusForbidden, code)
		code, _, _ = do(nil, http.MethodGet, base, nil)
		require.Equal(t, http.StatusUnauthorized, code)
	}

	// A token restricted to project A cannot read project B.
	tok := f.tokenCtx([]string{policy.ScopeLogsRead}, f.projA.ID)
	var ae *apierr.Error
	_, err := f.svc.ServiceFacets(tok, f.other.ID, FacetParams{})
	require.ErrorAs(t, err, &ae)
	require.Equal(t, http.StatusNotFound, ae.Status)
	_, err = f.svc.EnvironmentFacets(tok, f.other.EnvironmentID, FacetParams{})
	require.ErrorAs(t, err, &ae)
	require.Equal(t, http.StatusNotFound, ae.Status)
}
