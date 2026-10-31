package forward

import (
	"bytes"
	"encoding/base64"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strings"

	"github.com/nebulactrl/nebulactrl/pkg/agentprotocol"
)

// SeaweedFS builds filer paths by joining "/buckets/<bucket>" and the object
// key, and its IAM policy is evaluated on the URL's bucket alone. An object name
// carrying ".." segments would therefore walk out of the bucket if it reached
// the filer uncleaned, so every place a request can name an object is held to
// the same rule as the URL path (splitS3Path): the ?delete body and the query
// values prefix, start-after, marker, key-marker, continuation-token and the
// upload ids.
const (
	// maxDeleteBody bounds a bulk-delete body, and maxDeleteKeys the keys in it
	// (S3's own limit is 1,000 keys).
	maxDeleteBody = 1 << 20
	maxDeleteKeys = 1000
)

var (
	uploadIDRe      = regexp.MustCompile(`^[A-Za-z0-9_-]{1,128}$`)
	errBodyTooLarge = errors.New("request body is larger than declared")
)

// s3KeyValueParams are the query parameters whose value is an object name or a
// prefix of one.
var s3KeyValueParams = []string{"prefix", "start-after", "marker", "key-marker"}

// checkObjectName refuses a name that could leave the bucket: the object key
// rules of ValidObjectKey (no empty, "." or ".." segment, no leading "/", no
// backslash or NUL) and no control characters or surrounding blanks.
func checkObjectName(key string) error {
	if err := agentprotocol.ValidObjectKey(key); err != nil {
		return err
	}
	if key != strings.TrimSpace(key) {
		return fmt.Errorf("invalid object key %q", key)
	}
	for _, c := range key {
		if c < 0x20 || c == 0x7f {
			return fmt.Errorf("invalid object key %q", key)
		}
	}
	return nil
}

// checkKeyPrefix is checkObjectName for a listing prefix or marker: it may be
// empty and may end in one "/".
func checkKeyPrefix(v string) error {
	if v == "" {
		return nil
	}
	return checkObjectName(strings.TrimSuffix(v, "/"))
}

// checkNoWalk is the part of the rule that stops a name leaving the bucket,
// without the shape rules of a stored key (used where SeaweedFS may hand the
// client a value of its own making, such as a continuation token).
func checkNoWalk(v string) error {
	if len(v) > 2048 || strings.ContainsAny(v, "\\\x00") || strings.HasPrefix(v, "/") {
		return errors.New("invalid value")
	}
	for _, seg := range strings.Split(v, "/") {
		if seg == "." || seg == ".." {
			return errors.New("invalid value")
		}
	}
	return nil
}

// checkS3QueryValues validates every value (a parameter can repeat, and the
// service may read any of them) of the parameters that carry names or ids.
func checkS3QueryValues(rawQuery string) error {
	q, err := url.ParseQuery(rawQuery)
	if err != nil {
		return fmt.Errorf("malformed query: %w", err)
	}
	for _, k := range s3KeyValueParams {
		for _, v := range q[k] {
			if err := checkKeyPrefix(v); err != nil {
				return fmt.Errorf("%s: %w", k, err)
			}
		}
	}
	for _, v := range q["uploadId"] {
		if !uploadIDRe.MatchString(v) {
			return errors.New("uploadId is not a valid upload id")
		}
	}
	for _, v := range q["upload-id-marker"] {
		if v != "" && !uploadIDRe.MatchString(v) {
			return errors.New("upload-id-marker is not a valid upload id")
		}
	}
	for _, v := range q["continuation-token"] {
		// SeaweedFS derives the token from an object name (base64); a client
		// cannot be trusted to hand back what it was given, so the raw value and
		// what it decodes to must both be free of dot segments.
		if err := checkNoWalk(v); err != nil {
			return fmt.Errorf("continuation-token: %w", err)
		}
		for _, enc := range []*base64.Encoding{base64.StdEncoding, base64.URLEncoding, base64.RawStdEncoding, base64.RawURLEncoding} {
			if dec, err := enc.DecodeString(v); err == nil {
				if err := checkNoWalk(string(dec)); err != nil {
					return fmt.Errorf("continuation-token: %w", err)
				}
				break
			}
		}
	}
	return nil
}

// checkDeleteBody reads the body of a bulk delete (bounded), refuses it if any
// key could leave the bucket, and puts the same bytes back as the request body
// so the signature (which covers their hash) still verifies. An aws-chunked body
// is refused: the framing would have to be decoded here exactly as the service
// decodes it.
func checkDeleteBody(r *http.Request) error {
	if r.Header.Get("Content-Encoding") != "" || strings.HasPrefix(r.Header.Get("X-Amz-Content-Sha256"), "STREAMING-") {
		return errors.New("a bulk delete must be sent with a plain signed payload")
	}
	var body []byte
	if r.Body != nil {
		var err error
		body, err = io.ReadAll(io.LimitReader(r.Body, maxDeleteBody+1))
		if err != nil {
			return fmt.Errorf("read body: %w", err)
		}
	}
	if len(body) > maxDeleteBody {
		return errors.New("the delete request is too large")
	}
	if err := checkDeleteKeys(body); err != nil {
		return err
	}
	r.Body = io.NopCloser(bytes.NewReader(body))
	r.ContentLength = int64(len(body))
	r.TransferEncoding = nil
	return nil
}

// checkDeleteKeys walks the XML and checks the text of every <Key> element, at
// any depth.
func checkDeleteKeys(body []byte) error {
	dec := xml.NewDecoder(bytes.NewReader(body))
	var (
		key   strings.Builder
		inKey bool
		keys  int
	)
	for {
		tok, err := dec.Token()
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			return fmt.Errorf("malformed delete request: %w", err)
		}
		switch t := tok.(type) {
		case xml.StartElement:
			if t.Name.Local == "Key" {
				if inKey {
					return errors.New("malformed delete request")
				}
				inKey = true
				key.Reset()
			}
		case xml.CharData:
			if inKey {
				key.Write(t)
			}
		case xml.EndElement:
			if t.Name.Local == "Key" && inKey {
				inKey = false
				if keys++; keys > maxDeleteKeys {
					return errors.New("too many keys in one delete request")
				}
				if err := checkObjectName(key.String()); err != nil {
					return err
				}
			}
		}
	}
}

// limitBody passes at most left bytes, then fails with errBodyTooLarge without
// delivering the byte that crossed the limit.
type limitBody struct {
	rc       io.ReadCloser
	left     int64
	exceeded bool
}

func (b *limitBody) Read(p []byte) (int, error) {
	if b.exceeded {
		return 0, errBodyTooLarge
	}
	if int64(len(p)) > b.left+1 {
		p = p[:b.left+1]
	}
	n, err := b.rc.Read(p)
	if int64(n) > b.left {
		b.exceeded = true
		n = int(b.left)
		b.left = 0
		return n, errBodyTooLarge
	}
	b.left -= int64(n)
	return n, err
}

func (b *limitBody) Close() error { return b.rc.Close() }
