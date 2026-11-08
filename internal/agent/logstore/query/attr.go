package query

// Readers for the facet counts of the history UI. They expose what the matcher
// already derives, so a facet value counted here is exactly one that `key:value`
// matches; none of them changes matching.

// Facet returns the built-in facet of the line (service, level, stream, replica,
// status, deployment) as the matcher sees it; ok is false when the line has none.
func (l *Line) Facet(key string) (value string, ok bool) { return l.facetValue(key) }

// Attrs returns the parsed JSON or logfmt fields of the line (nested objects
// stay nested; an access-log line gains status, method and path). The map is
// shared with the line: do not modify it.
func (l *Line) Attrs() map[string]any { return l.fields().attrs }

// AttrText is a string, number or boolean attribute value as text, the way
// `@attr:value` writes it (numbers like JavaScript's String(n)); ok is false for
// null, objects and arrays.
func AttrText(v any) (text string, ok bool) {
	switch x := v.(type) {
	case string:
		return x, true
	case float64:
		return jsNumString(x), true
	case bool:
		if x {
			return "true", true
		}
		return "false", true
	}
	return "", false
}

// AttrNumber is the number an attribute value stands for in `@attr:>5`: a number,
// or a non-blank string that reads as one.
func AttrNumber(v any) (n float64, ok bool) { return numberOf(v) }
