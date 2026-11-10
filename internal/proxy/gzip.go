package proxy

import (
	"compress/gzip"
	"io"
	"net/http"
)

// UnwrapIdentityGzip decodes a gzip response body when the request pinned
// Accept-Encoding: identity. The transport only auto-decompresses a body it
// compressed the request for, so an identity request that a non-compliant
// upstream answers with Content-Encoding: gzip would otherwise reach the
// caller compressed and unreadable. Go strips the header when it decompressed
// the body itself, so this cannot double-decode.
//
// Call it after client.Do on every request that pinned identity — the shared
// doRequestOnce path and the provider lanes that build their own requests.
func UnwrapIdentityGzip(req *http.Request, resp *http.Response) {
	if req == nil || resp == nil || resp.Body == nil {
		return
	}
	if req.Header.Get("Accept-Encoding") != "identity" {
		return
	}
	if resp.Header.Get("Content-Encoding") != "gzip" {
		return
	}
	gz, err := gzip.NewReader(resp.Body)
	if err != nil {
		return
	}
	resp.Body = &gzipIdentityBody{Reader: gz, underlying: resp.Body}
	resp.Header.Del("Content-Encoding")
	resp.ContentLength = -1
	resp.Uncompressed = true
}

// gzipIdentityBody pairs a gzip reader with the body it decodes, so closing
// either releases both.
type gzipIdentityBody struct {
	*gzip.Reader
	underlying io.Closer
}

func (b *gzipIdentityBody) Close() error {
	gzErr := b.Reader.Close()
	underlyingErr := b.underlying.Close()
	if gzErr != nil {
		return gzErr
	}
	return underlyingErr
}
