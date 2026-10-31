package projectconfig

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"math"
	"reflect"
	"sort"
	"strconv"
	"time"
)

// maxCanonicalDepth stops the encoder on self-referential values.
const maxCanonicalDepth = 100

var fingerprintTimeType = reflect.TypeOf(time.Time{})

// Fingerprint returns "sha256:<hex>" of the canonical JSON of v. The
// fingerprint of a value never reveals the value, and two values that mean the
// same thing fingerprint the same:
//
//   - pointers and interfaces are followed; a nil pointer inside a struct omits
//     the field, anywhere else it is null;
//   - nil and empty slices are [], nil and empty maps are {};
//   - map keys and struct fields are sorted by name; struct fields use the
//     name of their json tag (omitempty is ignored, "-" and unexported fields
//     are skipped), so zero values stay in the encoding;
//   - integers print in decimal, integral floats print like integers (1.0 and 1
//     agree) and -0 is 0;
//   - time.Time is its UTC RFC 3339 text; kinds JSON cannot express (chan,
//     func, complex, unsafe pointers) encode as the name of the kind.
func Fingerprint(v any) string {
	sum := sha256.Sum256(canonicalJSON(v))
	return "sha256:" + hex.EncodeToString(sum[:])
}

// canonicalJSON is the byte string Fingerprint hashes.
func canonicalJSON(v any) []byte {
	return appendCanonical(nil, reflect.ValueOf(v), 0)
}

func appendCanonical(b []byte, v reflect.Value, depth int) []byte {
	if depth > maxCanonicalDepth {
		return appendJSONString(b, "depth")
	}
	switch v.Kind() {
	case reflect.Invalid:
		return append(b, "null"...)
	case reflect.Pointer, reflect.Interface:
		if v.IsNil() {
			return append(b, "null"...)
		}
		return appendCanonical(b, v.Elem(), depth+1)
	case reflect.Bool:
		return strconv.AppendBool(b, v.Bool())
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		return strconv.AppendInt(b, v.Int(), 10)
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64, reflect.Uintptr:
		return strconv.AppendUint(b, v.Uint(), 10)
	case reflect.Float32, reflect.Float64:
		return appendFloat(b, v.Float(), v.Type().Bits())
	case reflect.String:
		return appendJSONString(b, v.String())
	case reflect.Slice, reflect.Array:
		b = append(b, '[')
		for i := range v.Len() {
			if i > 0 {
				b = append(b, ',')
			}
			b = appendCanonical(b, v.Index(i), depth+1)
		}
		return append(b, ']')
	case reflect.Map:
		return appendCanonicalMap(b, v, depth)
	case reflect.Struct:
		return appendCanonicalStruct(b, v, depth)
	}
	return appendJSONString(b, v.Kind().String())
}

func appendCanonicalMap(b []byte, v reflect.Value, depth int) []byte {
	type entry struct {
		key string
		val []byte
	}
	entries := make([]entry, 0, v.Len())
	for it := v.MapRange(); it.Next(); {
		entries = append(entries, entry{
			key: mapKeyString(it.Key()),
			val: appendCanonical(nil, it.Value(), depth+1),
		})
	}
	sort.Slice(entries, func(i, j int) bool {
		if entries[i].key != entries[j].key {
			return entries[i].key < entries[j].key
		}
		return bytes.Compare(entries[i].val, entries[j].val) < 0
	})
	b = append(b, '{')
	for i, e := range entries {
		if i > 0 {
			b = append(b, ',')
		}
		b = appendJSONString(b, e.key)
		b = append(b, ':')
		b = append(b, e.val...)
	}
	return append(b, '}')
}

// mapKeyString is the text a map key is written as: strings as they are,
// integers in decimal, anything else by kind.
func mapKeyString(k reflect.Value) string {
	for k.Kind() == reflect.Pointer || k.Kind() == reflect.Interface {
		if k.IsNil() {
			return "null"
		}
		k = k.Elem()
	}
	switch k.Kind() {
	case reflect.String:
		return k.String()
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		return strconv.FormatInt(k.Int(), 10)
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64, reflect.Uintptr:
		return strconv.FormatUint(k.Uint(), 10)
	case reflect.Bool:
		return strconv.FormatBool(k.Bool())
	}
	return k.Kind().String()
}

func appendCanonicalStruct(b []byte, v reflect.Value, depth int) []byte {
	t := v.Type()
	if t == fingerprintTimeType && v.CanInterface() {
		return appendJSONString(b, v.Interface().(time.Time).UTC().Format(time.RFC3339Nano))
	}
	type field struct {
		name string
		val  reflect.Value
	}
	fields := make([]field, 0, t.NumField())
	for i := range t.NumField() {
		sf := t.Field(i)
		if !sf.IsExported() {
			continue
		}
		name, ok := jsonFieldName(sf)
		if !ok {
			continue
		}
		fv := v.Field(i)
		if fv.Kind() == reflect.Pointer && fv.IsNil() {
			continue
		}
		fields = append(fields, field{name, fv})
	}
	sort.SliceStable(fields, func(i, j int) bool { return fields[i].name < fields[j].name })
	b = append(b, '{')
	for i, f := range fields {
		if i > 0 {
			b = append(b, ',')
		}
		b = appendJSONString(b, f.name)
		b = append(b, ':')
		b = appendCanonical(b, f.val, depth+1)
	}
	return append(b, '}')
}

// jsonFieldName is the name of a struct field in the encoding: its json tag
// name, or the Go name when the tag has none. Fields tagged "-" are skipped.
func jsonFieldName(sf reflect.StructField) (string, bool) {
	tag := sf.Tag.Get("json")
	if tag == "-" {
		return "", false
	}
	for i := range len(tag) {
		if tag[i] == ',' {
			tag = tag[:i]
			break
		}
	}
	if tag == "" {
		return sf.Name, true
	}
	return tag, true
}

// appendFloat prints a float so that equal numbers agree with integers: an
// integral value below 1e15 prints as an integer, -0 prints as 0, and NaN and
// the infinities (which JSON cannot express) print as strings.
func appendFloat(b []byte, f float64, bits int) []byte {
	switch {
	case math.IsNaN(f):
		return appendJSONString(b, "NaN")
	case math.IsInf(f, 1):
		return appendJSONString(b, "+Inf")
	case math.IsInf(f, -1):
		return appendJSONString(b, "-Inf")
	case f == 0:
		return append(b, '0')
	case f == math.Trunc(f) && math.Abs(f) < 1e15:
		return strconv.AppendInt(b, int64(f), 10)
	}
	return strconv.AppendFloat(b, f, 'g', -1, bits)
}

func appendJSONString(b []byte, s string) []byte {
	enc, _ := json.Marshal(s) // marshalling a string cannot fail
	return append(b, enc...)
}
