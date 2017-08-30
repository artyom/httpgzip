package gziphandler

import (
	"compress/gzip"
	"io/ioutil"
	"net/http"
	"strconv"
	"strings"
	"sync"
)

const compressThreshold = 1000

func New(h http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Add("Vary", "Accept-Encoding")
		if acceptsGzip(r) {
			z := gzipWrap(w)
			defer z.Close()
			h.ServeHTTP(z, r)
			return
		}
		h.ServeHTTP(w, r)
	})
}

func gzipWrap(w http.ResponseWriter) *gRW {
	return &gRW{w: w}
}

type gRW struct {
	w    http.ResponseWriter
	z    *gzip.Writer
	skip bool
}

func (g *gRW) init() {
	if g.skip || g.z != nil {
		return
	}
	if g.w.Header().Get("Content-Encoding") != "" {
		g.skip = true
		return
	}
	if cl := g.w.Header().Get("Content-Length"); cl != "" {
		if n, err := strconv.Atoi(cl); err == nil && n < compressThreshold {
			g.skip = true
			return
		}
	}
	if !supportedContentType(g.w.Header().Get("Content-Type")) {
		g.skip = true
		return
	}
	g.z = pool.Get().(*gzip.Writer)
	g.z.Reset(g.w)
	g.w.Header().Set("Content-Encoding", "gzip")
	g.w.Header().Del("Content-Length")
}

func (g *gRW) Header() http.Header { return g.w.Header() }
func (g *gRW) WriteHeader(code int) {
	if g.z == nil && code != http.StatusNoContent && code != http.StatusNotModified {
		g.init()
	}
	g.w.WriteHeader(code)
}

func (g *gRW) Write(b []byte) (int, error) {
	if g.z == nil {
		g.init()
	}
	if g.skip {
		return g.w.Write(b)
	}
	if g.w.Header().Get("Content-Type") == "" {
		g.w.Header().Set("Content-Type", http.DetectContentType(b))
	}
	return g.z.Write(b)
}

func (g *gRW) Flush() {
	if g.z != nil {
		g.z.Flush()
	}
	if f, ok := g.w.(http.Flusher); ok {
		f.Flush()
	}
}

func (g *gRW) Close() {
	if g.z == nil {
		return
	}
	g.z.Close()
	if f, ok := g.w.(http.Flusher); ok {
		f.Flush()
	}
	pool.Put(g.z)
	g.z = nil
}

// acceptsGzip returns true if the given HTTP request indicates that it will
// accept a gzipped response.
func acceptsGzip(r *http.Request) bool {
	return allowsGzip(r.Header.Get("Accept-Encoding"))
}

func allowsGzip(hdr string) bool {
	if !strings.Contains(hdr, "gzip") {
		return false
	}
	for _, ss := range strings.Split(hdr, ",") {
		parts := strings.SplitN(ss, ";", 2)
		if l := len(parts); l == 0 || strings.TrimSpace(parts[0]) != "gzip" {
			continue
		} else if l == 1 {
			return true
		}
		p := strings.TrimSpace(parts[1])
		if qv := strings.TrimPrefix(p, "q="); qv != p {
			if q, err := strconv.ParseFloat(qv, 64); err == nil {
				return q != 0
			} else {
				return false
			}
		}
		return false
	}
	return false
}

func supportedContentType(s string) bool {
	if s == "" {
		return false
	}
	if strings.HasPrefix(s, "text/") {
		return true
	}
	if strings.HasPrefix(s, "application/") && (strings.Contains(s, "json") ||
		strings.Contains(s, "javascript") ||
		strings.Contains(s, "xml")) {
		return true
	}
	return false
}

var pool = sync.Pool{
	New: func() interface{} { return gzip.NewWriter(ioutil.Discard) },
}
