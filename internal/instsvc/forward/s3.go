package forward

import (
	"encoding/xml"
	"errors"
	"fmt"
	"net/http"
	"net/http/httputil"
	"strconv"
	"strings"

	"github.com/nebulactrl/nebulactrl/internal/instsvc"
)

// QuotaHeader is set on the response that refuses a write for lack of quota;
// the backup result handlers recognize it.
const QuotaHeader = "nebulactrl-quota-exceeded"

// QuotaMessage is the plain reason a full slice gives.
const QuotaMessage = "built-in backup storage is full"

// s3Query is every query parameter a backup client may send. Anything else is a
// sub-resource this proxy does not serve (?policy, ?acl, ?tagging, ?lifecycle,
// ?versioning, ?seaweedfs-quota, ...): refused, not forwarded.
var s3Query = map[string]bool{
	// listing
	"list-type": true, "prefix": true, "delimiter": true, "max-keys": true, "continuation-token": true,
	"start-after": true, "marker": true, "encoding-type": true, "fetch-owner": true, "location": true,
	// multipart
	"uploads": true, "uploadId": true, "partNumber": true, "part-number-marker": true, "max-parts": true,
	"key-marker": true, "upload-id-marker": true, "max-uploads": true,
	// bulk delete
	"delete": true,
	// SDK bookkeeping and response overrides
	"x-id": true, "response-content-type": true, "response-content-disposition": true, "response-cache-control": true,
	"response-content-encoding": true, "response-content-language": true, "response-expires": true,
	// presigned authentication
	"X-Amz-Algorithm": true, "X-Amz-Credential": true, "X-Amz-Date": true, "X-Amz-Expires": true,
	"X-Amz-SignedHeaders": true, "X-Amz-Signature": true, "X-Amz-Security-Token": true,
}

type s3Handler struct {
	f     *Forwarder
	proxy *httputil.ReverseProxy
}

func newS3(f *Forwarder, raw string) (http.Handler, error) {
	target, err := parseBase(raw)
	if err != nil {
		return nil, err
	}
	h := &s3Handler{f: f}
	h.proxy = newProxy(target, f.log, nil, nil, func(w http.ResponseWriter, r *http.Request, _ error) {
		if lb, ok := r.Body.(*limitBody); ok && lb.exceeded {
			s3Error(w, http.StatusBadRequest, "EntityTooLarge", "The request body is larger than its declared size.")
			return
		}
		s3Error(w, http.StatusBadGateway, "InternalError", "the built-in storage is not reachable")
	})
	return h, nil
}

type s3ErrorBody struct {
	XMLName xml.Name `xml:"Error"`
	Code    string   `xml:"Code"`
	Message string   `xml:"Message"`
}

func s3Error(w http.ResponseWriter, status int, code, msg string) {
	w.Header().Set("Content-Type", "application/xml")
	w.WriteHeader(status)
	_, _ = w.Write([]byte(xml.Header))
	_ = xml.NewEncoder(w).Encode(s3ErrorBody{Code: code, Message: msg})
}

func denyS3(w http.ResponseWriter) {
	s3Error(w, http.StatusForbidden, "AccessDenied", "Access Denied.")
}

// accessKeyOf extracts the access key id a request is signed with.
func accessKeyOf(r *http.Request) (string, bool) {
	cred := ""
	if a := r.Header.Get("Authorization"); a != "" {
		rest, ok := strings.CutPrefix(a, "AWS4-HMAC-SHA256")
		if !ok {
			return "", false
		}
		for _, part := range strings.Split(rest, ",") {
			part = strings.TrimSpace(part)
			if v, ok := strings.CutPrefix(part, "Credential="); ok {
				cred = v
			}
		}
	} else if v := r.URL.Query().Get("X-Amz-Credential"); v != "" {
		cred = v
	}
	ak, _, ok := strings.Cut(cred, "/")
	return ak, ok && ak != ""
}

// s3Op is what a request does, as far as policy cares.
type s3Op struct {
	// write is true when the request stores bytes (PutObject, UploadPart).
	write bool
	// bulkDelete is true for POST /<bucket>?delete: the object names are in the body.
	bulkDelete bool
}

// classifyS3 validates the request against what a backup needs and says whether
// it stores data. bucket is the first path segment, key the rest.
func classifyS3(r *http.Request, bucket, key string) (s3Op, error) {
	q := r.URL.Query()
	for k := range q {
		if !s3Query[k] {
			return s3Op{}, fmt.Errorf("sub-resource %q is not served", k)
		}
	}
	if r.Header.Get("X-Amz-Copy-Source") != "" {
		return s3Op{}, errNotImplemented
	}
	has := func(k string) bool { _, ok := q[k]; return ok }
	if key == "" { // bucket level
		switch r.Method {
		case http.MethodHead:
			return s3Op{}, nil
		case http.MethodGet:
			return s3Op{}, nil
		case http.MethodPost:
			if has("delete") {
				return s3Op{bulkDelete: true}, nil
			}
		}
		return s3Op{}, errors.New("bucket operation " + r.Method + " is not served")
	}
	switch r.Method {
	case http.MethodGet, http.MethodHead:
		return s3Op{}, nil
	case http.MethodPut:
		return s3Op{write: true}, nil
	case http.MethodPost:
		if has("uploads") || has("uploadId") {
			return s3Op{}, nil
		}
	case http.MethodDelete:
		return s3Op{}, nil
	}
	return s3Op{}, errors.New("object operation " + r.Method + " is not served")
}

var errNotImplemented = errors.New("not implemented")

// splitS3Path splits a path-style request path into bucket and key, refusing
// dot segments (a proxy and a server that disagree on cleaning them would
// disagree on the bucket).
func splitS3Path(p string) (bucket, key string, ok bool) {
	p = strings.TrimPrefix(p, "/")
	bucket, key, _ = strings.Cut(p, "/")
	if bucket == "" {
		return "", "", false
	}
	for _, seg := range strings.Split(p, "/") {
		if seg == ".." || seg == "." {
			return "", "", false
		}
	}
	return bucket, key, true
}

func (h *s3Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	pe := peerOf(ctx)
	deny := func(why string, args ...any) {
		h.f.log.Warn("s3 request refused", append([]any{"team_id", pe.team.String(), "server_id", pe.server.String(), "method", r.Method, "path", r.URL.Path, "why", why}, args...)...)
		denyS3(w)
	}
	ak, ok := accessKeyOf(r)
	if !ok {
		deny("not signed")
		return
	}
	owner, err := h.f.keyOwner(ctx, ak)
	if err != nil || owner != pe.team {
		deny("key is not this team's")
		return
	}
	ts, err := h.f.teamSlices(ctx, pe.team)
	if err != nil {
		s3Error(w, http.StatusServiceUnavailable, "ServiceUnavailable", "the built-in storage is not available")
		return
	}
	sl := ts.S3
	if sl == nil || sl.AccessKey != ak {
		deny("no such slice or retired key")
		return
	}
	if sl.State != "ready" && sl.State != "full" {
		s3Error(w, http.StatusServiceUnavailable, "ServiceUnavailable", "the built-in storage is not ready")
		return
	}
	bucket, key, ok := splitS3Path(r.URL.Path)
	if !ok || bucket != sl.Ref {
		deny("bucket is not this team's", "bucket", bucket)
		return
	}
	if err := checkS3QueryValues(r.URL.RawQuery); err != nil {
		deny(err.Error())
		return
	}
	op, err := classifyS3(r, bucket, key)
	switch {
	case errors.Is(err, errNotImplemented):
		s3Error(w, http.StatusNotImplemented, "NotImplemented", "Copying objects is not supported on built-in backup storage.")
		return
	case err != nil:
		deny(err.Error())
		return
	}
	if op.bulkDelete {
		if err := checkDeleteBody(r); err != nil {
			deny(err.Error())
			return
		}
	}
	if op.write {
		n, limit, err := writeSize(r)
		if err != nil {
			s3Error(w, http.StatusBadRequest, "InvalidRequest", err.Error())
			return
		}
		release, err := h.f.d.Gate.Admit(ctx, pe.team, n)
		switch {
		case errors.Is(err, instsvc.ErrQuotaExceeded):
			w.Header().Set(QuotaHeader, QuotaMessage)
			s3Error(w, http.StatusForbidden, "QuotaExceeded", "The "+QuotaMessage+".")
			return
		case errors.Is(err, instsvc.ErrDiskLow):
			w.Header().Set("Retry-After", "300")
			s3Error(w, http.StatusInsufficientStorage, "InsufficientStorage", "the host running the built-in storage is low on disk space")
			return
		case err != nil:
			h.f.log.Error("quota gate", "team_id", pe.team.String(), "err", err)
			s3Error(w, http.StatusServiceUnavailable, "ServiceUnavailable", "the built-in storage is not available")
			return
		}
		defer release()
		// The admitted size is what was declared; what is forwarded must not exceed it.
		r.Body = &limitBody{rc: r.Body, left: limit}
	}
	h.proxy.ServeHTTP(w, r)
}

// chunkOverhead is the room the aws-chunked framing (chunk headers, signatures,
// the trailer) may take on top of the decoded size: 128 bytes for each 4 KiB
// of payload, the smallest chunk any SDK uses, plus a fixed allowance.
func chunkOverhead(n int64) int64 { return (n/4096+1)*128 + 1024 }

// writeSize is what a PutObject or UploadPart may store. n is the number of
// payload bytes the quota gate admits; limit is the most bytes the body may
// carry on the wire.
//
// A plain body has n = limit = Content-Length. Only an aws-chunked body (its
// x-amz-content-sha256 starts with STREAMING-) has a decoded length that differs
// from Content-Length, and then n is x-amz-decoded-content-length and the body
// may carry n plus the framing. A body without a Content-Length (chunked
// transfer encoding) is refused: its size is unknown until it has been stored.
func writeSize(r *http.Request) (n, limit int64, err error) {
	if r.ContentLength < 0 || len(r.TransferEncoding) > 0 {
		return 0, 0, errors.New("a Content-Length is required; chunked transfer encoding is not accepted")
	}
	decoded := r.Header.Get("X-Amz-Decoded-Content-Length")
	if !strings.HasPrefix(r.Header.Get("X-Amz-Content-Sha256"), "STREAMING-") {
		if decoded != "" || strings.Contains(strings.ToLower(r.Header.Get("Content-Encoding")), "aws-chunked") {
			return 0, 0, errors.New("an aws-chunked body needs a STREAMING-* x-amz-content-sha256")
		}
		return r.ContentLength, r.ContentLength, nil
	}
	if decoded == "" {
		return 0, 0, errors.New("x-amz-decoded-content-length is required for a streaming payload")
	}
	n, err = strconv.ParseInt(decoded, 10, 64)
	if err != nil || n < 0 {
		return 0, 0, errors.New("x-amz-decoded-content-length is not a number")
	}
	limit = n + chunkOverhead(n)
	if r.ContentLength > limit {
		return 0, 0, errors.New("the body is larger than x-amz-decoded-content-length allows")
	}
	return n, limit, nil
}
