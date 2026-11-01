package backup

import (
	"bufio"
	"bytes"
	"errors"
	"io"
	"regexp"
	"strings"
)

// A plain SQL dump written by another server names that server's roles,
// databases and grants. Restoring it into a managed database must put
// everything into the managed database and leave the cluster alone, so the
// statements below are removed from the stream before the engine's client sees
// them. A statement is only recognised when it is a single line that starts in
// column 0 (that is how pg_dump, mysqldump and mariadb-dump write them), and
// never inside a COPY data block.
//
// The dump is also untrusted input for the client itself: psql and mysql run
// programs and read or write files in the database container on behalf of
// lines such as `\! cmd`, `\i file`, `\o file`, `\copy ... program`, `system`,
// `source` or `tee`. Those commands are not tied to column 0 (psql takes them
// anywhere outside a quoted string or comment), so the filters follow the
// client's own lexing (quotes, dollar quotes, comments, COPY data) and remove
// every line that holds a command the client would act on.

// errUnsafeSQL ends a restore whose stream holds something that cannot be
// dropped line by line.
var errUnsafeSQL = errors.New("the dump contains client commands (shell, file or program access) that a restore must not run")

// sqlFilterBuffer is the longest line looked at as a whole; longer lines (rows
// of extended INSERTs) are scanned chunk by chunk and otherwise passed through
// untouched.
const sqlFilterBuffer = 256 << 10

// sqlFilter is an io.Reader that applies rewrite to every line of r.
//
// rewrite gets a whole line (including its newline) with whole set, or the
// first chunk of a longer line without it. It returns the bytes to pass on,
// drops the line (and the rest of it) when drop is set, or fails the stream
// with err. cont sees the further chunks of a long line whose start was
// already passed on; all it can do is refuse the stream.
type sqlFilter struct {
	br      *bufio.Reader
	rewrite func(line []byte, whole bool) (out []byte, drop bool, err error)
	cont    func(chunk []byte) error
	out     []byte
	mid     bool // inside a line longer than the buffer
	skip    bool // inside a long line that was dropped
	err     error
	dropped int
}

func newSQLFilter(r io.Reader, rewrite func([]byte, bool) ([]byte, bool, error), cont func([]byte) error) *sqlFilter {
	return &sqlFilter{br: bufio.NewReaderSize(r, sqlFilterBuffer), rewrite: rewrite, cont: cont}
}

// Dropped is the number of statements removed so far.
func (f *sqlFilter) Dropped() int { return f.dropped }

// Err is the failure that ended the stream early: an unsafe dump or a read
// error of the source. It is nil at a regular end of the input.
func (f *sqlFilter) Err() error {
	if f == nil || errors.Is(f.err, io.EOF) {
		return nil
	}
	return f.err
}

func (f *sqlFilter) Read(p []byte) (int, error) {
	for len(f.out) == 0 {
		if f.err != nil {
			return 0, f.err
		}
		line, err := f.br.ReadSlice('\n')
		endsLine := !errors.Is(err, bufio.ErrBufferFull) // a newline, EOF or a read error
		if err != nil && endsLine {
			f.err = err
		}
		if len(line) > 0 {
			f.take(line, endsLine)
		}
		if endsLine {
			f.mid, f.skip = false, false
		}
	}
	n := copy(p, f.out)
	f.out = f.out[n:]
	return n, nil
}

// take passes line (a whole line, or a chunk of a long one) through the
// filter into f.out.
func (f *sqlFilter) take(line []byte, endsLine bool) {
	switch {
	case f.skip: // the rest of a dropped long line
	case f.mid: // the rest of a long line whose start was passed on
		if err := f.cont(line); err != nil {
			f.err = err
			return
		}
		f.out = append(f.out[:0], line...)
	default:
		out, drop, err := f.rewrite(line, endsLine)
		switch {
		case err != nil:
			f.err = err
		case drop:
			f.dropped++
			f.skip = !endsLine
		default:
			f.out = append(f.out[:0], out...)
			f.mid = !endsLine
		}
	}
}

// foldEqual compares b with s ignoring ASCII case.
func foldEqual(b []byte, s string) bool {
	if len(b) != len(s) {
		return false
	}
	for i := range b {
		c := b[i]
		if 'A' <= c && c <= 'Z' {
			c += 'a' - 'A'
		}
		if c != s[i] {
			return false
		}
	}
	return true
}

func hasFoldPrefix(b []byte, prefix string) bool {
	return len(b) >= len(prefix) && strings.EqualFold(string(b[:len(prefix)]), prefix)
}

// --- PostgreSQL ---

// pgSkip lists the statement prefixes (upper case) that refer to roles,
// ownership, privileges or other databases.
var pgSkip = []string{
	"GRANT ", "REVOKE ", "ALTER DEFAULT PRIVILEGES ",
	"SET SESSION AUTHORIZATION ", "RESET SESSION AUTHORIZATION",
	"CREATE ROLE ", "ALTER ROLE ", "DROP ROLE ", "CREATE USER ", "ALTER USER ", "DROP USER ", "CREATE GROUP ", "ALTER GROUP ", "DROP GROUP ",
	"CREATE TABLESPACE ", "DROP TABLESPACE ",
	"CREATE DATABASE ", "DROP DATABASE ", "ALTER DATABASE ", "COMMENT ON DATABASE ",
	"ALTER SYSTEM ",
}

var (
	// pg_dump 17.6+ writes \restrict KEY / \unrestrict KEY around a dump; they
	// only limit what psql may do, so they are the one meta-command kept.
	pgRestrictRe = regexp.MustCompile(`^\\(un)?restrict[ \t]+[A-Za-z0-9]+[ \t]*\r?\n?$`)
	// pgStdStringsOnRe is the only way a dump may touch
	// standard_conforming_strings: with it off, the client lexes backslashes in
	// strings differently from this filter.
	pgStdStringsOnRe = regexp.MustCompile(`(?i)^[ \t]*SET[ \t]+((SESSION|LOCAL)[ \t]+)?standard_conforming_strings[ \t]*(=|TO)[ \t]*(on|true)[ \t]*;[ \t]*\r?\n?$`)
)

// changesStringSyntax reports a statement that could switch
// standard_conforming_strings off.
func changesStringSyntax(line []byte) bool {
	t := bytes.TrimLeft(line, " \t")
	if !hasFoldPrefix(t, "SET ") && !hasFoldPrefix(t, "SET\t") && !hasFoldPrefix(t, "SELECT ") && !hasFoldPrefix(t, "ALTER ") && !hasFoldPrefix(t, "DO ") {
		return false
	}
	if !strings.Contains(strings.ToLower(string(t)), "standard_conforming_strings") {
		return false
	}
	return !pgStdStringsOnRe.Match(line)
}

type pgState uint8

const (
	pgCode       pgState = iota
	pgQuote              // inside '...'
	pgGap                // after a closing quote: a doubled quote or a continuation may follow
	pgGapDash            // in that gap after one "-"
	pgGapLine            // in that gap inside a "--" comment
	pgDQuote             // inside "..."
	pgDollarOpen         // after "$": reading a tag
	pgDollar             // inside $tag$ ... $tag$
	pgLine               // inside a "--" comment
	pgBlock              // inside /* ... */ (nests)
)

// maxDollarTag bounds a dollar-quote tag; a longer one is refused rather than
// guessed at.
const maxDollarTag = 256

// pgScanner follows psql's lexer far enough to tell code from strings,
// comments and COPY data. It is fed bytes in any chunking: every lookahead is
// a state.
type pgScanner struct {
	state   pgState
	escape  bool // the current string is E'...' (backslash escapes)
	bs      bool // in E'...': the previous byte was a backslash
	inIdent bool // inside an identifier or keyword
	eStart  bool // the previous byte was an e/E that starts an identifier
	last    byte // previous byte of code (for "--" and "/*") or of a block comment
	depth   int  // comment nesting
	tag     []byte
	delim   []byte // the open dollar quote, with both dollars
	match   int    // bytes of delim matched at the end of the data so far
	gapNL   bool   // the gap holds a newline: the string continues after it
	gapLen  int
	inCopy  bool // COPY ... FROM stdin data follows
	copyBS  bool // in COPY data: the previous byte was a backslash
}

func isIdentStart(c byte) bool {
	return c == '_' || c >= 0x80 || ('a' <= c && c <= 'z') || ('A' <= c && c <= 'Z')
}

func isIdentCont(c byte) bool {
	return isIdentStart(c) || c == '$' || ('0' <= c && c <= '9')
}

// endToken marks a token boundary after a quoted construct or comment.
func (s *pgScanner) endToken() {
	s.inIdent, s.eStart, s.last = false, false, 0
}

// skip returns how many bytes of b cannot change the state (-1 for all of
// them).
func (s *pgScanner) skip(b []byte) int {
	switch s.state {
	case pgQuote:
		if s.bs {
			return 0
		}
		if s.escape {
			return bytes.IndexAny(b, "'\\")
		}
		return bytes.IndexByte(b, '\'')
	case pgDQuote:
		return bytes.IndexByte(b, '"')
	case pgLine:
		return bytes.IndexAny(b, "\r\n")
	case pgDollar:
		if s.match == 0 {
			return bytes.IndexByte(b, '$')
		}
	}
	return 0
}

// scan advances over b and reports whether psql would see a backslash command
// in it (a backslash in code), or whether the input cannot be followed.
func (s *pgScanner) scan(b []byte) bool {
	for i := 0; i < len(b); i++ {
		j := s.skip(b[i:])
		if j < 0 {
			return false
		}
		i += j
		for redo := true; redo; {
			var cmd bool
			redo, cmd = s.step(b[i])
			if cmd {
				return true
			}
		}
	}
	return false
}

// step consumes c. redo asks for c to be fed again in the new state; cmd
// reports a backslash command (or input that cannot be followed).
func (s *pgScanner) step(c byte) (redo, cmd bool) {
	switch s.state {
	case pgQuote:
		switch {
		case s.bs:
			s.bs = false
		case s.escape && c == '\\':
			s.bs = true
		case c == '\'':
			s.state, s.gapNL, s.gapLen = pgGap, false, 0
		}
		return false, false
	case pgGap:
		// A string goes on across a newline (also past comment lines), and
		// '' is a quote inside it; anything else ends it.
		switch c {
		case '\'':
			if s.gapLen != 0 && !s.gapNL {
				s.escape = false // a new, standard string
			}
			s.state = pgQuote
			return false, false
		case ' ', '\t', '\f':
			s.gapLen++
			return false, false
		case '\n', '\r':
			s.gapNL = true
			s.gapLen++
			return false, false
		case '-':
			s.state = pgGapDash
			s.gapLen++
			return false, false
		}
		s.endToken()
		s.state = pgCode
		return true, false
	case pgGapDash:
		if c == '-' {
			s.state = pgGapLine
			return false, false
		}
		s.endToken()
		s.state = pgCode
		return true, false
	case pgGapLine:
		if c == '\n' || c == '\r' {
			s.state, s.gapNL = pgGap, true
		}
		return false, false
	case pgDQuote:
		if c == '"' {
			s.endToken()
			s.state = pgCode
		}
		return false, false
	case pgLine:
		if c == '\n' || c == '\r' {
			s.endToken()
			s.state = pgCode
		}
		return false, false
	case pgBlock:
		switch {
		case c == '/' && s.last == '*':
			s.last = 0
			if s.depth--; s.depth == 0 {
				s.endToken()
				s.state = pgCode
			}
		case c == '*' && s.last == '/':
			s.last = 0
			s.depth++
		default:
			s.last = c
		}
		return false, false
	case pgDollarOpen:
		switch {
		case c == '$':
			s.delim = append(append([]byte{'$'}, s.tag...), '$')
			s.state, s.match = pgDollar, 0
		case isIdentStart(c) || (len(s.tag) > 0 && '0' <= c && c <= '9'):
			if len(s.tag) >= maxDollarTag {
				return false, true
			}
			s.tag = append(s.tag, c)
		default: // not a dollar quote ($1, $name)
			s.endToken()
			s.state = pgCode
			return true, false
		}
		return false, false
	case pgDollar:
		if s.match > 0 {
			if c == s.delim[s.match] {
				s.match++
				if s.match == len(s.delim) {
					s.match = 0
					s.endToken()
					s.state = pgCode
				}
				return false, false
			}
			s.match = 0
		}
		if c == '$' {
			s.match = 1
		}
		return false, false
	}

	// pgCode
	switch c {
	case '\\':
		return false, true
	case '\'':
		s.escape, s.bs = s.eStart, false
		s.endToken()
		s.state = pgQuote
		return false, false
	case '"':
		s.endToken()
		s.state = pgDQuote
		return false, false
	case '$':
		if !s.inIdent {
			s.endToken()
			s.state, s.tag = pgDollarOpen, nil
			return false, false
		}
	case '-':
		if s.last == '-' {
			s.endToken()
			s.state = pgLine
			return false, false
		}
	case '*':
		if s.last == '/' {
			s.endToken()
			s.state, s.depth = pgBlock, 1
			return false, false
		}
	}
	switch {
	case s.inIdent:
		s.eStart = false
		if !isIdentCont(c) {
			s.inIdent = false
		}
	case isIdentStart(c):
		s.inIdent = true
		s.eStart = c == 'e' || c == 'E'
	default:
		s.eStart = false
	}
	s.last = c
	return false, false
}

// copyData checks a line of COPY data. pg_dump writes only the escapes
// \N \\ \t \n \r \b \f \v \x.. and \ooo there; any other backslash would be a
// command if psql were not in COPY mode (this scanner and psql can disagree
// about where a COPY starts), so it is refused instead of passed on.
func (s *pgScanner) copyData(b []byte) bool {
	for _, c := range b {
		if s.copyBS {
			s.copyBS = false
			if strings.IndexByte("N\\tnrbfvx01234567", c) < 0 {
				return false
			}
			continue
		}
		if c == '\\' {
			s.copyBS = true
		}
	}
	return true
}

// newPGSQLFilter strips ownership, privilege, role and database statements
// and every psql meta-command (except \restrict and \unrestrict) from a plain
// pg_dump stream.
func newPGSQLFilter(r io.Reader) *sqlFilter {
	sc := &pgScanner{}
	rewrite := func(line []byte, whole bool) ([]byte, bool, error) {
		if sc.inCopy {
			if whole && bytes.Equal(bytes.TrimRight(line, "\r\n"), []byte(`\.`)) {
				sc.inCopy, sc.copyBS = false, false
				return line, false, nil
			}
			if !sc.copyData(line) {
				return nil, false, errUnsafeSQL
			}
			return line, false, nil
		}
		if len(line) == 0 {
			return line, false, nil
		}
		if whole && sc.state == pgCode && line[0] == '\\' && pgRestrictRe.Match(line) {
			return line, false, nil
		}
		snap := *sc
		if sc.scan(line) || (whole && snap.state == pgCode && changesStringSyntax(line)) {
			*sc = snap
			return nil, true, nil
		}
		if !whole {
			return line, false, nil
		}
		head := line
		if len(head) > 96 {
			head = head[:96]
		}
		up := strings.ToUpper(string(head))
		if strings.HasPrefix(up, "COPY ") && bytes.Contains(line, []byte("FROM stdin")) && snap.state == pgCode && sc.state == pgCode {
			sc.inCopy = true
			return line, false, nil
		}
		if !bytes.HasSuffix(bytes.TrimRight(line, " \r\n"), []byte(";")) {
			return line, false, nil
		}
		for _, p := range pgSkip {
			if strings.HasPrefix(up, p) {
				*sc = snap
				return nil, true, nil
			}
		}
		if strings.HasPrefix(up, "ALTER ") && bytes.Contains(bytes.ToUpper(line), []byte(" OWNER TO ")) {
			*sc = snap
			return nil, true, nil
		}
		return line, false, nil
	}
	cont := func(chunk []byte) error {
		if sc.inCopy {
			if !sc.copyData(chunk) {
				return errUnsafeSQL
			}
			return nil
		}
		if sc.scan(chunk) {
			return errUnsafeSQL
		}
		return nil
	}
	return newSQLFilter(r, rewrite, cont)
}

// --- MySQL / MariaDB ---

var (
	mysqlDefiner = regexp.MustCompile("(?i)DEFINER\\s*=\\s*(`[^`]*`|[^\\s@`*]+)@(`[^`]*`|[^\\s`*]+)")
	mysqlSkipRe  = regexp.MustCompile(`(?i)^(CREATE\s+DATABASE|DROP\s+DATABASE|USE\s|/\*![0-9]+\s+DROP\s+DATABASE|/\*![0-9]+\s+CREATE\s+DATABASE)`)
	// mysqlDelimiterRe is a DELIMITER command that stands alone on its line
	// (mysqldump writes `DELIMITER ;;` around routines).
	mysqlDelimiterRe = regexp.MustCompile(`(?i)^[ \t]*delimiter[ \t]+(\S+)[ \t]*\r?\n?$`)
)

// mysqlClientCommands are the client commands that run programs, read or write
// files or leave the target database; the client takes them as the first word
// of a statement.
var mysqlClientCommands = []string{"system", "source", "tee", "pager", "edit", "connect", "use", "quit", "exit"}

// validMySQLDelimiter limits DELIMITER to what this scanner can follow: no
// quotes, comment starters or backslashes (the client refuses those too),
// except the common `//`.
func validMySQLDelimiter(d string) bool {
	if d == "//" {
		return true
	}
	if len(d) == 0 || len(d) > 8 {
		return false
	}
	for i := range len(d) {
		if strings.IndexByte(";$|!%^&~@:", d[i]) < 0 {
			return false
		}
	}
	return true
}

// setsNoBackslashEscapes reports a statement that could switch the session to
// NO_BACKSLASH_ESCAPES, after which the client lexes strings differently from
// this filter.
func setsNoBackslashEscapes(trimmed []byte) bool {
	if !hasFoldPrefix(trimmed, "SET ") && !hasFoldPrefix(trimmed, "SET\t") && !bytes.HasPrefix(trimmed, []byte("/*!")) {
		return false
	}
	return strings.Contains(strings.ToUpper(string(trimmed)), "NO_BACKSLASH_ESCAPES")
}

type mysqlState uint8

const (
	myCode       mysqlState = iota
	myStr                   // inside '...', "..." or `...`
	myLine                  // inside a # or -- comment
	myBlock                 // inside /* ... */
	myDash1                 // after "-"
	myDash2                 // after "--"
	mySlash                 // after "/"
	mySlashStar             // after "/*"
	mySlashStarM            // after "/*M"
)

// mysqlScanner follows the mysql/mariadb client's own line parser: the client
// takes a backslash outside a string as the start of a command, and the first
// word of a statement as a command name. Like pgScanner it is fed bytes in any
// chunking.
type mysqlScanner struct {
	state        mysqlState
	quote        byte
	bs           bool // the previous byte was a backslash
	star         bool // in a block comment: the previous byte was '*'
	open         bool // statement text was seen since the last delimiter
	word         [16]byte
	wn           int
	wordOn       bool // collecting the first word of a statement
	delim        []byte
	dm           int // bytes of delim matched at the end of the data so far
	cmd          bool
	sawDelimiter bool
}

func newMySQLScanner() *mysqlScanner { return &mysqlScanner{delim: []byte{';'}} }

func isMySpace(c byte) bool {
	return c == ' ' || c == '\t' || c == '\n' || c == '\r' || c == '\v' || c == '\f'
}

// text takes c as statement text; the first word of a statement is kept to be
// compared with the client commands.
func (s *mysqlScanner) text(c byte) {
	if isMySpace(c) {
		s.endWord()
		return
	}
	if !s.open {
		s.open, s.wordOn, s.wn = true, true, 0
	}
	if s.wordOn {
		if s.wn == len(s.word) {
			s.wordOn = false
			return
		}
		s.word[s.wn] = c
		s.wn++
	}
}

func (s *mysqlScanner) endWord() {
	if !s.wordOn {
		return
	}
	s.wordOn = false
	w := bytes.TrimRight(s.word[:s.wn], ";")
	if foldEqual(w, "delimiter") {
		s.sawDelimiter = true
		return
	}
	for _, name := range mysqlClientCommands {
		if foldEqual(w, name) {
			s.cmd = true
			return
		}
	}
}

func (s *mysqlScanner) setDelimiter(d string) {
	s.delim, s.dm = []byte(d), 0
	s.state, s.bs, s.open, s.wordOn = myCode, false, false, false
}

func (s *mysqlScanner) skip(b []byte) int {
	switch s.state {
	case myStr:
		if s.bs {
			return 0
		}
		for j, c := range b {
			if c == s.quote || c == '\\' {
				return j
			}
		}
		return -1
	case myLine:
		return bytes.IndexByte(b, '\n')
	}
	return 0
}

// scan advances over b and reports whether the client would run a command in
// it.
func (s *mysqlScanner) scan(b []byte) bool {
	for i := 0; i < len(b); i++ {
		j := s.skip(b[i:])
		if j < 0 {
			return false
		}
		i += j
		for redo := true; redo; {
			redo = s.step(b[i])
			if s.cmd {
				return true
			}
		}
	}
	return false
}

// step consumes c and reports whether it must be fed again in the new state.
func (s *mysqlScanner) step(c byte) (redo bool) {
	switch s.state {
	case myStr:
		switch {
		case s.bs:
			s.bs = false
		case c == '\\':
			s.bs = true
		case c == s.quote:
			s.state = myCode
		}
		return false
	case myLine:
		if c == '\n' {
			s.state = myCode
		}
		return false
	case myBlock:
		if s.star && c == '/' {
			s.state = myCode
		}
		s.star = c == '*'
		return false
	case myDash1:
		if c == '-' {
			s.state = myDash2
			return false
		}
		s.state = myCode
		s.text('-')
		return true
	case myDash2:
		if isMySpace(c) { // "-- " starts a comment
			s.endWord()
			s.state = myLine
			if c == '\n' {
				s.state = myCode
			}
			return false
		}
		s.state = myCode
		s.text('-')
		s.text('-')
		return true
	case mySlash:
		if c == '*' {
			s.state = mySlashStar
			return false
		}
		s.state = myCode
		s.text('/')
		return true
	case mySlashStar, mySlashStarM:
		if c == '!' { // /*! and /*M! are executable comments: code
			s.state = myCode
			s.text('/')
			s.text('*')
			s.text('!')
			return false
		}
		if c == 'M' && s.state == mySlashStar {
			s.state = mySlashStarM
			return false
		}
		s.endWord()
		s.state, s.star = myBlock, c == '*'
		return false
	}

	// myCode
	if s.dm > 0 && c != s.delim[s.dm] {
		// What matched so far was ordinary text. The only delimiter that can
		// be the start of something else is "//" (a "/*" comment).
		s.dm = 0
		if s.delim[0] == '/' {
			s.state = mySlash
			return true
		}
	}
	if s.bs {
		s.bs = false
		switch c {
		case 'N': // \N is NULL
			s.text('\\')
			s.text('N')
		case '-': // MariaDB's "/*M!999999\- ... */" line; not a command
		default:
			s.cmd = true
		}
		return false
	}
	if c == s.delim[s.dm] {
		s.dm++
		if s.dm == len(s.delim) {
			s.dm = 0
			s.endWord()
			s.open = false
		} else {
			s.text(c)
		}
		return false
	}
	switch c {
	case '\\':
		s.bs = true
	case '\'', '"', '`':
		s.text(c)
		s.endWord()
		s.state, s.quote, s.bs = myStr, c, false
	case '#':
		s.endWord()
		s.state = myLine
	case '-':
		s.state = myDash1
	case '/':
		s.state = mySlash
	default:
		s.text(c)
	}
	return false
}

// newMySQLFilter keeps a MySQL/MariaDB dump inside the target database: it
// removes the statements that create, drop or switch databases, the client
// commands that run programs or touch files, and the DEFINER clauses of views,
// routines, triggers and events (their definer account does not exist here).
func newMySQLFilter(r io.Reader) *sqlFilter {
	sc := newMySQLScanner()
	rewrite := func(line []byte, whole bool) ([]byte, bool, error) {
		trimmed := bytes.TrimLeft(line, " \t")
		if len(trimmed) == 0 {
			return line, false, nil
		}
		head := trimmed
		if len(head) > 128 {
			head = head[:128]
		}
		if whole && mysqlSkipRe.Match(head) && bytes.HasSuffix(bytes.TrimRight(line, " \r\n"), []byte(";")) {
			return nil, true, nil
		}
		if setsNoBackslashEscapes(trimmed) {
			return nil, true, nil
		}
		sc.sawDelimiter = false
		snap := *sc
		if sc.scan(line) {
			*sc = snap
			return nil, true, nil
		}
		if sc.sawDelimiter {
			m := mysqlDelimiterRe.FindSubmatch(line)
			if !whole || m == nil || !validMySQLDelimiter(string(m[1])) {
				*sc = snap
				return nil, true, nil
			}
			sc.setDelimiter(string(m[1]))
			return line, false, nil
		}
		if whole && (bytes.HasPrefix(head, []byte("/*!")) || hasFoldPrefix(head, "CREATE ") || hasFoldPrefix(head, "ALTER ")) {
			if mysqlDefiner.Match(line) {
				return mysqlDefiner.ReplaceAll(line, nil), false, nil
			}
		}
		return line, false, nil
	}
	cont := func(chunk []byte) error {
		sc.sawDelimiter = false
		if sc.scan(chunk) || sc.sawDelimiter {
			return errUnsafeSQL
		}
		return nil
	}
	return newSQLFilter(r, rewrite, cont)
}
