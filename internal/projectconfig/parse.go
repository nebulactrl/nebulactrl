package projectconfig

import (
	"bytes"
	"errors"
	"fmt"
	"path"
	"reflect"
	"sort"
	"strings"
	"unicode/utf8"

	toml "github.com/pelletier/go-toml/v2"
	"github.com/pelletier/go-toml/v2/unstable"
)

// Attachments are files the document refers to (stack.compose_file,
// files.<path>.source), keyed by the relative path as written in the file.
// The caller reads them; Parse does no I/O.
type Attachments map[string][]byte

// fileMeta is the parse state kept on a File.
type fileMeta struct {
	tree     map[string]any
	idx      *Index
	att      Attachments
	reported map[string]bool // diagKey (+ "\x00"+env) of everything Parse reported
	baseKeys map[string]bool // diagKey of the base view
	// overLimit is set when the document passed the limits pass with errors
	// (nesting depth): it was not decoded, so no view resolves its services.
	overLimit bool
}

// Parse strictly decodes text (size cap, UTF-8, one document, unknown keys,
// types, limits) and validates it statically: the file as written, the base
// view and every environment's resolved view, so a returned
// diagnostic list is complete for the document. Problems that exist only in
// one environment's view carry Environment. It returns a nil *File only when
// the text cannot be decoded at all (too large, bad encoding, TOML syntax).
func Parse(text []byte, att Attachments) (*File, *Index, Diagnostics) {
	if len(text) > MaxFileBytes {
		return nil, nil, Diagnostics{{
			Severity: SeverityError, Code: CodeFileTooLarge,
			Message: fmt.Sprintf("nebula.toml is %d bytes; the limit is %d bytes", len(text), MaxFileBytes),
		}}
	}
	if d, bad := checkEncoding(text); bad {
		return nil, nil, Diagnostics{d}
	}
	var tree map[string]any
	if err := toml.Unmarshal(text, &tree); err != nil {
		d := syntaxDiag(err)
		if d.Line == 0 {
			// Duplicate keys and tables are reported without a position.
			if p := BuildIndex(text).dupPos(); !p.IsZero() {
				d = d.WithPos(p)
			}
		}
		return nil, nil, Diagnostics{d}
	}
	if tree == nil {
		tree = map[string]any{}
	}
	idx := BuildIndex(text)
	s := &sink{pos: func(p string, value bool) Pos { return idx.at(p, value) }}
	f := &File{}
	f.meta = &fileMeta{tree: tree, idx: idx, att: att, reported: map[string]bool{}}

	dec := &decoder{s: s, loc: func(p []string) string { return PathString(p...) }, idx: idx}
	if !dec.walkLimits(tree, nil, 0) {
		f.meta.overLimit = true
		return f, idx, s.list
	}
	dec.structInto(reflect.ValueOf(f).Elem(), tree, nil)
	f.docChecks(s)
	checkAttachments(att, s)

	// Views: the base first, then every environment block.
	_, baseDiags := f.view("")
	f.meta.baseKeys = map[string]bool{}
	for _, d := range baseDiags {
		f.meta.baseKeys[diagKey(d)] = true
	}
	out := append(Diagnostics(nil), s.list...)
	out = append(out, baseDiags...)
	for _, env := range f.envBlocks() {
		_, ds := f.view(env)
		for _, d := range ds {
			if f.meta.baseKeys[diagKey(d)] {
				continue
			}
			d.Environment = env
			out = append(out, d)
		}
	}
	out = out.Dedup()
	for _, d := range out {
		k := diagKey(d)
		if d.Environment != "" {
			k += "\x00" + d.Environment
		}
		f.meta.reported[k] = true
	}
	return f, idx, out
}

// Environments lists the slugs of the [environments.<slug>] tables in the
// file, sorted. A table whose body failed to decode is still listed (Parse
// reported the problem); an entry that is not a table is not a block.
func (f *File) Environments() []string {
	out := []string{}
	if f.meta != nil {
		envs, _ := f.meta.tree["environments"].(map[string]any)
		for k, v := range envs {
			if _, ok := v.(map[string]any); ok {
				out = append(out, k)
			}
		}
	} else {
		for k := range f.EnvironmentBlocks {
			out = append(out, k)
		}
	}
	sort.Strings(out)
	return out
}

// envBlocks lists environment blocks including blocks whose body failed to
// decode (so every block in the tree is resolved and validated).
func (f *File) envBlocks() []string {
	if f.meta == nil {
		return f.Environments()
	}
	envs, _ := f.meta.tree["environments"].(map[string]any)
	out := make([]string, 0, len(envs))
	for k := range envs {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// AttachmentPaths lists the relative paths the file refers to (compose_file
// and files.<path>.source of every service, base and overrides), cleaned,
// deduplicated, in file order. Invalid paths are skipped; Parse reports them.
func (f *File) AttachmentPaths() []string {
	if f == nil || f.meta == nil {
		return nil
	}
	type ref struct {
		ord  int
		path string
	}
	var refs []ref
	seen := map[string]bool{}
	add := func(p []string, val any) {
		s, ok := val.(string)
		if !ok || checkRelPath(s) != "" {
			return
		}
		c := path.Clean(s)
		if seen[c] {
			return
		}
		seen[c] = true
		refs = append(refs, ref{f.meta.idx.order(PathString(p...)), c})
	}
	scan := func(prefix []string, svc map[string]any) {
		if st, ok := svc["stack"].(map[string]any); ok {
			if v, ok := st["compose_file"]; ok {
				add(append(append([]string(nil), prefix...), "stack", "compose_file"), v)
			}
		}
		if files, ok := svc["files"].(map[string]any); ok {
			for k, fv := range files {
				if fm, ok := fv.(map[string]any); ok {
					if v, ok := fm["source"]; ok {
						add(append(append([]string(nil), prefix...), "files", k, "source"), v)
					}
				}
			}
		}
	}
	if svcs, ok := f.meta.tree["services"].(map[string]any); ok {
		for slug, sv := range svcs {
			if m, ok := sv.(map[string]any); ok {
				scan([]string{"services", slug}, m)
			}
		}
	}
	if envs, ok := f.meta.tree["environments"].(map[string]any); ok {
		for e, ev := range envs {
			em, _ := ev.(map[string]any)
			svcs, _ := em["services"].(map[string]any)
			for slug, sv := range svcs {
				if m, ok := sv.(map[string]any); ok {
					scan([]string{"environments", e, "services", slug}, m)
				}
			}
		}
	}
	sort.SliceStable(refs, func(i, j int) bool {
		if refs[i].ord != refs[j].ord {
			return refs[i].ord < refs[j].ord
		}
		return refs[i].path < refs[j].path
	})
	out := make([]string, len(refs))
	for i, r := range refs {
		out[i] = r.path
	}
	return out
}

// checkRelPath returns a non-empty reason when p is not a safe relative path.
func checkRelPath(p string) string {
	switch {
	case p == "":
		return "the path is empty"
	case len(p) > 512:
		return "the path is longer than 512 bytes"
	case strings.ContainsRune(p, 0):
		return "the path contains a NUL byte"
	case strings.HasPrefix(p, "/") || strings.HasPrefix(p, "\\"):
		return "the path must be relative to nebula.toml"
	}
	for _, seg := range strings.FieldsFunc(p, func(r rune) bool { return r == '/' || r == '\\' }) {
		if seg == ".." {
			return "the path must not contain .."
		}
	}
	if strings.HasPrefix(path.Clean(p), "..") {
		return "the path must not leave the directory of nebula.toml"
	}
	if len(p) >= 2 && p[1] == ':' {
		return "the path must be relative to nebula.toml"
	}
	return ""
}

func checkEncoding(text []byte) (Diagnostic, bool) {
	var off int
	var why string
	switch {
	case bytes.HasPrefix(text, []byte{0xEF, 0xBB, 0xBF}):
		off, why = 0, "the file starts with a byte order mark; save it as UTF-8 without BOM"
	case bytes.IndexByte(text, 0) >= 0:
		off, why = bytes.IndexByte(text, 0), "the file contains a NUL byte"
	case !utf8.Valid(text):
		for i := 0; i < len(text); {
			r, n := utf8.DecodeRune(text[i:])
			if r == utf8.RuneError && n == 1 {
				off = i
				break
			}
			i += n
		}
		why = "the file is not valid UTF-8"
	default:
		return Diagnostic{}, false
	}
	d := Diagnostic{Severity: SeverityError, Code: CodeInvalidEncoding, Message: why}
	ix := BuildIndex(text)
	if p := ix.rangePos(unstable.Range{Offset: uint32(off), Length: 1}); !p.IsZero() {
		d = d.WithPos(p)
	}
	return d, true
}

func syntaxDiag(err error) Diagnostic {
	d := Diagnostic{Severity: SeverityError, Code: CodeTOMLSyntax, Message: "invalid TOML: " + err.Error()}
	var de *toml.DecodeError
	if errors.As(err, &de) {
		row, col := de.Position()
		d.Message = "invalid TOML: " + strings.TrimPrefix(de.Error(), "toml: ")
		d.Line, d.Column, d.EndLine, d.EndColumn = row, col, row, col+1
	}
	return d
}

func checkAttachments(att Attachments, s *sink) {
	if len(att) > MaxAttachments {
		s.errorf(CodeLimitExceeded, "", "%d attachments; at most %d are accepted", len(att), MaxAttachments)
	}
	total := 0
	names := make([]string, 0, len(att))
	for k := range att {
		names = append(names, k)
	}
	sort.Strings(names)
	for _, k := range names {
		if len(att[k]) > MaxAttachmentBytes {
			s.errorf(CodeLimitExceeded, "", "attachment %q is %d bytes; at most %d", k, len(att[k]), MaxAttachmentBytes)
		}
		total += len(att[k])
	}
	if total > MaxAttachmentsTotal {
		s.errorf(CodeLimitExceeded, "", "attachments total %d bytes; at most %d", total, MaxAttachmentsTotal)
	}
}
