package backups

import (
	"bytes"
	"compress/gzip"
	"fmt"
	"io"
	"strings"
	"unicode/utf8"

	"github.com/nebulactrl/nebulactrl/internal/apierr"
	"github.com/nebulactrl/nebulactrl/pkg/agentprotocol"
)

// SniffBytes is how much of a file's start the control plane inspects.
const SniffBytes = 16 << 10

// DumpKind is what the first bytes of an uploaded file say it is.
type DumpKind struct {
	// Format is one of agentprotocol.DumpPGCustom, DumpPGTar, DumpSQL, DumpMongo.
	Format string
	// Gzip: the file is a gzip stream of Format.
	Gzip bool
	// Dialect names the engine a plain SQL dump was written by ("postgres",
	// "mysql") when its header says so; empty when unknown.
	Dialect string
}

// DumpFormatLabel names a format for people.
func DumpFormatLabel(k DumpKind) string {
	var s string
	switch k.Format {
	case agentprotocol.DumpPGCustom:
		s = "PostgreSQL custom-format dump (pg_dump -Fc)"
	case agentprotocol.DumpPGTar:
		s = "PostgreSQL tar-format dump (pg_dump -Ft)"
	case agentprotocol.DumpMongo:
		s = "MongoDB archive (mongodump --archive)"
	case agentprotocol.DumpSQL:
		switch k.Dialect {
		case "postgres":
			s = "PostgreSQL plain SQL dump"
		case "mysql":
			s = "MySQL/MariaDB SQL dump"
		default:
			s = "plain SQL dump"
		}
	default:
		s = k.Format
	}
	if k.Gzip {
		s += ", gzip-compressed"
	}
	return s
}

func unsupportedFormat(format string, args ...any) error {
	return apierr.Unprocessable("unsupported_dump_format", fmt.Sprintf(format, args...))
}

// SniffDump classifies a dump file from its first bytes (at most SniffBytes
// are used). It only inspects magic numbers and the header of text dumps: it
// does not prove the file is complete.
func SniffDump(prefix []byte) (DumpKind, error) {
	if len(prefix) == 0 {
		return DumpKind{}, unsupportedFormat("the file is empty")
	}
	if len(prefix) > SniffBytes {
		prefix = prefix[:SniffBytes]
	}
	if len(prefix) >= 2 && prefix[0] == 0x1f && prefix[1] == 0x8b {
		inner, err := gunzipPrefix(prefix)
		if err != nil {
			return DumpKind{}, err
		}
		if len(inner) >= 2 && inner[0] == 0x1f && inner[1] == 0x8b {
			return DumpKind{}, unsupportedFormat("the file is gzip-compressed twice; decompress it once and upload it again")
		}
		k, err := sniffPlain(inner)
		if err != nil {
			return DumpKind{}, err
		}
		k.Gzip = true
		return k, nil
	}
	return sniffPlain(prefix)
}

// gunzipPrefix returns the first decompressed bytes of a (truncated) gzip
// stream.
func gunzipPrefix(prefix []byte) ([]byte, error) {
	zr, err := gzip.NewReader(bytes.NewReader(prefix))
	if err != nil {
		return nil, unsupportedFormat("the file starts like gzip but is not a valid gzip file: %v", err)
	}
	defer zr.Close()
	buf := make([]byte, 8<<10)
	n, err := io.ReadFull(zr, buf)
	if n == 0 && err != nil && err != io.EOF && err != io.ErrUnexpectedEOF {
		return nil, unsupportedFormat("the gzip file is damaged: %v", err)
	}
	if n == 0 {
		return nil, unsupportedFormat("the gzip file holds no data")
	}
	return buf[:n], nil
}

func sniffPlain(b []byte) (DumpKind, error) {
	switch {
	case bytes.HasPrefix(b, []byte("PGDMP")):
		return DumpKind{Format: agentprotocol.DumpPGCustom}, nil
	case len(b) >= 262 && string(b[257:262]) == "ustar":
		return DumpKind{Format: agentprotocol.DumpPGTar}, nil
	case bytes.HasPrefix(b, []byte{0x6d, 0xe2, 0x99, 0x81}):
		return DumpKind{Format: agentprotocol.DumpMongo}, nil
	case bytes.HasPrefix(b, []byte("REDIS")):
		return DumpKind{}, unsupportedFormat("this is a Redis/Valkey RDB file; restoring a key-value dump is not supported")
	case bytes.HasPrefix(b, []byte("SQLite format 3\x00")):
		return DumpKind{}, unsupportedFormat("this is a SQLite database file, not a dump")
	case bytes.HasPrefix(b, []byte{0x28, 0xb5, 0x2f, 0xfd}):
		return DumpKind{}, unsupportedFormat("zstd-compressed dumps are not supported; decompress it or use gzip")
	case bytes.HasPrefix(b, []byte{0xfd, '7', 'z', 'X', 'Z', 0}):
		return DumpKind{}, unsupportedFormat("xz-compressed dumps are not supported; decompress it or use gzip")
	case bytes.HasPrefix(b, []byte("BZh")):
		return DumpKind{}, unsupportedFormat("bzip2-compressed dumps are not supported; decompress it or use gzip")
	case bytes.HasPrefix(b, []byte("PK\x03\x04")):
		return DumpKind{}, unsupportedFormat("zip archives are not supported; upload the dump file itself (or gzip it)")
	case bytes.HasPrefix(b, []byte{0xff, 0xfe}) || bytes.HasPrefix(b, []byte{0xfe, 0xff}):
		return DumpKind{}, unsupportedFormat("the file is UTF-16 encoded (a PowerShell > redirect does that); export it as UTF-8 and upload it again")
	}
	if bytes.IndexByte(b, 0) >= 0 {
		return DumpKind{}, unsupportedFormat("unrecognised file: not a pg_dump (custom, tar or plain SQL), mysqldump or mongodump file")
	}
	// Text: the cut may end inside a multi-byte rune.
	t := b
	for i := 0; i < utf8.UTFMax && len(t) > 0 && !utf8.Valid(t); i++ {
		t = t[:len(t)-1]
	}
	if !utf8.Valid(t) {
		return DumpKind{}, unsupportedFormat("unrecognised file: it is not UTF-8 text and not a pg_dump or mongodump archive")
	}
	head := strings.ToLower(string(t))
	k := DumpKind{Format: agentprotocol.DumpSQL}
	switch {
	case strings.Contains(head, "postgresql database cluster dump"):
		return DumpKind{}, unsupportedFormat("this is a pg_dumpall cluster dump; dump the single database with pg_dump instead")
	case strings.Contains(head, "-- postgresql database dump"):
		k.Dialect = "postgres"
	case strings.Contains(head, "-- mysql dump"), strings.Contains(head, "-- mariadb dump"), strings.Contains(head, "/*!40101 set @old_character_set_client"),
		strings.Contains(head, "/*m!999999"):
		k.Dialect = "mysql"
	}
	if s := strings.TrimSpace(head); strings.HasPrefix(s, "{") || strings.HasPrefix(s, "[") {
		return DumpKind{}, unsupportedFormat("the file looks like JSON, not a database dump")
	}
	return k, nil
}

// CheckDumpEngine verifies that a dump of kind can be restored into a database
// of engine. The messages say what the file is and where it belongs.
func CheckDumpEngine(engine string, k DumpKind) error {
	if !agentprotocol.DumpFormatSupported(engine, k.Format) {
		return unsupportedFormat("this is a %s; it cannot be restored into a %s database%s", DumpFormatLabel(k), engineLabel(engine), wantsHint(engine))
	}
	if k.Format == agentprotocol.DumpSQL && k.Dialect != "" {
		switch {
		case engine == "postgres" && k.Dialect != "postgres":
			return unsupportedFormat("this is a %s; it cannot be restored into a PostgreSQL database", DumpFormatLabel(k))
		case (engine == "mysql" || engine == "mariadb") && k.Dialect != "mysql":
			return unsupportedFormat("this is a %s; it cannot be restored into a %s database", DumpFormatLabel(k), engineLabel(engine))
		}
	}
	return nil
}

func engineLabel(engine string) string {
	switch engine {
	case "postgres":
		return "PostgreSQL"
	case "mysql":
		return "MySQL"
	case "mariadb":
		return "MariaDB"
	case "mongodb":
		return "MongoDB"
	}
	return engine
}

func wantsHint(engine string) string {
	switch engine {
	case "postgres":
		return " (expected pg_dump -Fc, -Ft or plain SQL)"
	case "mysql", "mariadb":
		return " (expected a plain SQL dump from mysqldump or mariadb-dump)"
	case "mongodb":
		return " (expected mongodump --archive)"
	}
	return ""
}
