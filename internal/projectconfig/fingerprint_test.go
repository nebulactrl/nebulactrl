package projectconfig

import (
	"math"
	"regexp"
	"testing"
	"time"
)

var fingerprintRe = regexp.MustCompile(`^sha256:[0-9a-f]{64}$`)

type fpSample struct {
	B int            `json:"b"`
	A []string       `json:"a"`
	C *int           `json:"c"`
	D map[string]int `json:"d"`
	E string         `json:"-"`
	f int
}

type fpCycle struct {
	N *fpCycle `json:"n"`
}

func TestCanonicalJSON(t *testing.T) {
	five := 5
	one := 1
	zero := time.Date(2024, 1, 2, 3, 4, 5, 0, time.UTC)
	tests := []struct {
		name string
		in   any
		want string
	}{
		{"nil", nil, `null`},
		{"nil pointer", (*int)(nil), `null`},
		{"nil map", map[string]int(nil), `{}`},
		{"empty map", map[string]int{}, `{}`},
		{"nil slice", []string(nil), `[]`},
		{"empty slice", []string{}, `[]`},
		{"struct omits nil pointer, keeps empty collections, skips - and unexported",
			fpSample{B: 1, E: "hidden", f: 9}, `{"a":[],"b":1,"d":{}}`},
		{"struct with pointer set", fpSample{B: 1, C: &five}, `{"a":[],"b":1,"c":5,"d":{}}`},
		{"pointer to struct", &fpSample{B: 1}, `{"a":[],"b":1,"d":{}}`},
		{"nil pointers in slice are null", []*int{nil, &one}, `[null,1]`},
		{"map keys sorted", map[string]int{"b": 2, "a": 1, "c": 3}, `{"a":1,"b":2,"c":3}`},
		{"int keys sorted as text", map[int]string{10: "x", 2: "y"}, `{"10":"x","2":"y"}`},
		{"string escaping", "a\"b\n", `"a\"b\n"`},
		{"unicode string", "grüße 日本", `"grüße 日本"`},
		{"bool", true, `true`},
		{"negative int8", int8(-5), `-5`},
		{"max uint64", uint64(math.MaxUint64), `18446744073709551615`},
		{"array", [2]int{1, 2}, `[1,2]`},
		{"interface slice", []any{1, "a", nil, true, 2.0}, `[1,"a",null,true,2]`},
		{"nested map in map", map[string]any{"z": map[string]any{"b": 1, "a": 2}, "y": []int{}}, `{"y":[],"z":{"a":2,"b":1}}`},
		{"untagged field uses Go name", struct{ X int }{1}, `{"X":1}`},
		{"tag without name uses Go name", struct {
			X int `json:",omitempty"`
		}{0}, `{"X":0}`},
		{"omitempty is ignored", struct {
			X int    `json:"x,omitempty"`
			S string `json:"s,omitempty"`
		}{}, `{"s":"","x":0}`},
		{"fields sorted by json name", struct {
			Z int `json:"a"`
			A int `json:"z"`
		}{1, 2}, `{"a":1,"z":2}`},

		{"float integral", 1.0, `1`},
		{"float fraction", 2.5, `2.5`},
		{"float tenth", 0.1, `0.1`},
		{"float32 tenth", float32(0.1), `0.1`},
		{"float negative zero", math.Copysign(0, -1), `0`},
		{"float large integral", 1e14, `100000000000000`},
		{"float exponent", 1e21, `1e+21`},
		{"float NaN", math.NaN(), `"NaN"`},
		{"float +Inf", math.Inf(1), `"+Inf"`},
		{"float -Inf", math.Inf(-1), `"-Inf"`},

		{"time UTC", zero, `"2024-01-02T03:04:05Z"`},
		{"time in zone is the same instant", zero.In(time.FixedZone("x", 3600)), `"2024-01-02T03:04:05Z"`},
		{"time inside struct", struct {
			T time.Time `json:"t"`
		}{zero}, `{"t":"2024-01-02T03:04:05Z"}`},

		{"chan", make(chan int), `"chan"`},
		{"func", func() {}, `"func"`},
		{"complex", complex(1, 2), `"complex128"`},
		{"struct with chan field", struct {
			C chan int `json:"c"`
		}{}, `{"c":"chan"}`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := string(canonicalJSON(tt.in)); got != tt.want {
				t.Fatalf("canonicalJSON = %s, want %s", got, tt.want)
			}
		})
	}
}

func TestCanonicalJSONCycleTerminates(t *testing.T) {
	c := &fpCycle{}
	c.N = c
	if got := canonicalJSON(c); len(got) == 0 {
		t.Fatal("empty encoding of a cyclic value")
	}
	if fp := Fingerprint(c); !fingerprintRe.MatchString(fp) {
		t.Fatalf("Fingerprint = %q", fp)
	}
}

func TestFingerprintFormat(t *testing.T) {
	for _, v := range []any{nil, 0, "", "x", []int{1}, map[string]string{"a": "b"}, fpSample{}, Variable{Value: "x"}} {
		if got := Fingerprint(v); !fingerprintRe.MatchString(got) {
			t.Errorf("Fingerprint(%#v) = %q, want sha256:<64 hex>", v, got)
		}
	}
}

func TestFingerprintEqual(t *testing.T) {
	five := 5
	manyA := map[string]int{}
	manyB := map[string]int{}
	keys := make([]string, 0, 64)
	for i := range 64 {
		keys = append(keys, string(rune('a'+i%26))+string(rune('A'+i/26))+"k")
	}
	for i, k := range keys {
		manyA[k] = i
	}
	for i := len(keys) - 1; i >= 0; i-- {
		manyB[keys[i]] = i
	}
	type sameShape struct {
		Second int    `json:"b"`
		First  string `json:"a"`
	}

	tests := []struct {
		name string
		a, b any
	}{
		{"nil and empty slice", []string(nil), []string{}},
		{"nil and empty slice in struct", fpSample{A: nil}, fpSample{A: []string{}}},
		{"nil and empty map", map[string]int(nil), map[string]int{}},
		{"nil and empty map in struct", fpSample{D: nil}, fpSample{D: map[string]int{}}},
		{"pointer and value", &fpSample{B: 3}, fpSample{B: 3}},
		{"pointer to pointer", new(new(7)), 7},
		{"pointer field and value inside slice", []*int{&five}, []int{5}},
		{"pointer field value", fpSample{C: &five}, fpSample{C: new(5)}},
		{"map insertion order", manyA, manyB},
		{"float and int", 1.0, 1},
		{"float and uint", float64(42), uint8(42)},
		{"negative zero and zero", math.Copysign(0, -1), 0.0},
		{"float32 and float64 of a short decimal", float32(0.5), 0.5},
		{"interface and concrete", []any{1, "a"}, []any{int64(1), "a"}},
		{"map and struct with the same json shape",
			map[string]any{"b": 1, "a": "x"}, sameShape{Second: 1, First: "x"}},
		{"ignored fields", fpSample{B: 1, E: "one", f: 1}, fpSample{B: 1, E: "two", f: 2}},
		{"time zones", time.Date(2024, 5, 1, 12, 0, 0, 0, time.UTC), time.Date(2024, 5, 1, 14, 0, 0, 0, time.FixedZone("x", 7200))},
		{"nil interface and nil pointer", nil, (*int)(nil)},
		{"struct pointer field omitted like missing key", struct {
			P *int `json:"p"`
		}{}, struct{}{}},
		{"variable by value and by pointer", Variable{Value: "${{db.URL}}", Scope: "build"}, &Variable{Value: "${{db.URL}}", Scope: "build"}},
		{"nil and empty collections in a service",
			Service{Command: nil, Variables: nil}, Service{Command: []string{}, Variables: map[string]Variable{}}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fa, fb := Fingerprint(tt.a), Fingerprint(tt.b)
			if fa != fb {
				t.Fatalf("fingerprints differ:\n  %s = %s\n  %s = %s", canonicalJSON(tt.a), fa, canonicalJSON(tt.b), fb)
			}
		})
	}
}

func TestFingerprintDiffers(t *testing.T) {
	tests := []struct {
		name string
		a, b any
	}{
		{"different ints", 1, 2},
		{"string and int", "1", 1},
		{"string and null", "", nil},
		{"empty slice and null", []string{}, nil},
		{"empty slice and empty map", []string{}, map[string]string{}},
		{"split strings", []string{"a", "b"}, []string{"ab"}},
		{"slice order", []int{1, 2}, []int{2, 1}},
		{"bool", true, false},
		{"map value", map[string]int{"a": 1}, map[string]int{"a": 2}},
		{"map key", map[string]int{"a": 1}, map[string]int{"b": 1}},
		{"map key and value swapped", map[string]string{"a": "b"}, map[string]string{"b": "a"}},
		{"fraction", 0.5, 0.25},
		{"nil pointer field and pointer to zero", struct {
			P *int `json:"p"`
		}{}, struct {
			P *int `json:"p"`
		}{new(0)}},
		{"zero field and missing field", struct {
			X int `json:"x,omitempty"`
		}{}, struct{}{}},
		{"nested change", map[string]any{"a": []any{map[string]any{"b": 1}}}, map[string]any{"a": []any{map[string]any{"b": 2}}}},
		{"nil pointer element and zero", []*int{nil}, []int{0}},
		{"nan and inf", math.NaN(), math.Inf(1)},
		{"time instants", time.Date(2024, 5, 1, 12, 0, 0, 0, time.UTC), time.Date(2024, 5, 1, 12, 0, 1, 0, time.UTC)},
		{"escaped quote", `a"b`, `a\"b`},
		{"variable value", Variable{Value: "a"}, Variable{Value: "b"}},
		{"variable scope", Variable{Value: "a"}, Variable{Value: "a", Scope: "build"}},
		{"variable public flag", Variable{Value: "a"}, Variable{Value: "a", Public: true}},
		{"route option set differently",
			RouteOptions{ForceHTTPS: new(true)}, RouteOptions{Compress: new(true)}},
		{"route option true and false",
			RouteOptions{ForceHTTPS: new(true)}, RouteOptions{ForceHTTPS: new(false)}},
		{"domain with and without options", Domain{}, Domain{Options: &RouteOptions{}}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fa, fb := Fingerprint(tt.a), Fingerprint(tt.b)
			if fa == fb {
				t.Fatalf("fingerprints are equal (%s) for %s and %s", fa, canonicalJSON(tt.a), canonicalJSON(tt.b))
			}
		})
	}
}

func TestFingerprintStable(t *testing.T) {
	v := map[string]any{"x": []int{1, 2, 3}, "y": fpSample{B: 1}}
	first := Fingerprint(v)
	for range 20 {
		if got := Fingerprint(v); got != first {
			t.Fatalf("fingerprint changed between calls: %s vs %s", first, got)
		}
	}
}

func TestFingerprintNeverPanicsOnOddKinds(t *testing.T) {
	var nilIface any
	var nilFunc func()
	values := []any{
		make(chan int), func() {}, nilFunc, complex64(1), nilIface,
		map[any]any{1: "a", "1": "b", nil: "c", true: "d"},
		map[string]any{"c": make(chan string)},
		[]any{make(chan int), func() {}},
		struct{ U uintptr }{7},
	}
	for _, v := range values {
		if got := Fingerprint(v); !fingerprintRe.MatchString(got) {
			t.Errorf("Fingerprint(%T) = %q", v, got)
		}
	}
}

func TestFingerprintMapKeyCollisionIsDeterministic(t *testing.T) {
	// 1 and "1" print as the same key; the order must not depend on map iteration.
	m := map[any]string{1: "a", "1": "b"}
	first := Fingerprint(m)
	for range 50 {
		if got := Fingerprint(m); got != first {
			t.Fatalf("fingerprint depends on map iteration order: %s vs %s", first, got)
		}
	}
}
