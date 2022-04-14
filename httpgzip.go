// Package httpgzip provides a wrapper to http.Handler that does on the fly gzip
// encoding if certain conditions are met.
//
// Content is compressed only if client understands it, content size is greater
// than certain threshold and content type matches predefined list of types.
package httpgzip

import (
	"compress/gzip"
	"io"
	"net/http"
	"strconv"
	"strings"
	"sync"
)

const compressThreshold = 1000

const (
	hdrAcceptEncoding  = "Accept-Encoding"
	hdrContentEncoding = "Content-Encoding"
	hdrContentType     = "Content-Type"
	hdrContentLength   = "Content-Length"
	hdrContentRange    = "Content-Range"
)

// Option functions are used to configure new handler.
type Option func(*gzipHandler)

// WithLevel configures handler to use specified compression level. It will
// panic if level is not one of the values accepted by gzip.NewWriterLevel.
func WithLevel(level int) Option {
	if _, err := gzip.NewWriterLevel(io.Discard, level); err != nil {
		panic(err)
	}
	return func(g *gzipHandler) { g.writerPool = newWriterPool(level) }
}

// New returns a http.Handler that optionally compresses response using
// 'Content-Enconding: gzip' scheme.
func New(h http.Handler, options ...Option) http.Handler {
	g := &gzipHandler{
		h:          h,
		writerPool: newWriterPool(gzip.BestSpeed),
	}
	for _, fn := range options {
		fn(g)
	}
	return g
}

type gzipHandler struct {
	h          http.Handler
	writerPool writerPool
}

func (h *gzipHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	w.Header().Add("Vary", hdrAcceptEncoding)
	if !acceptsGzip(r) {
		h.h.ServeHTTP(w, r)
		return
	}
	z := &gRW{w: w, pool: h.writerPool}
	defer z.Close()
	h.h.ServeHTTP(z, r)
}

type gRW struct {
	w           http.ResponseWriter
	z           *gzip.Writer
	pool        writerPool
	skip        bool
	wroteHeader bool // whether WriteHeader was called
}

func (g *gRW) init() {
	if g.skip || g.z != nil {
		return
	}
	if g.w.Header().Get(hdrContentRange) != "" {
		g.skip = true
		return
	}
	if g.w.Header().Get(hdrContentEncoding) != "" {
		g.skip = true
		return
	}
	if cl := g.w.Header().Get(hdrContentLength); cl != "" {
		if n, err := strconv.Atoi(cl); err == nil && n < compressThreshold {
			g.skip = true
			return
		}
	}
	if ct := g.w.Header().Get(hdrContentType); ct != "" && !supportedContentType(ct) {
		g.skip = true
		return
	}
	g.z = g.pool.Get()
	g.z.Reset(g.w)
	g.w.Header().Set(hdrContentEncoding, "gzip")
	g.w.Header().Del(hdrContentLength)
}

func (g *gRW) Header() http.Header { return g.w.Header() }
func (g *gRW) WriteHeader(code int) {
	g.wroteHeader = true
	if g.z == nil && code != http.StatusNoContent && code != http.StatusNotModified &&
		code != http.StatusPartialContent {
		g.init()
	}
	g.w.WriteHeader(code)
}

func (g *gRW) Write(b []byte) (int, error) {
	if !g.wroteHeader {
		if g.w.Header().Get(hdrContentType) == "" {
			g.w.Header().Set(hdrContentType, http.DetectContentType(b))
		}
		g.WriteHeader(http.StatusOK)
	}
	if g.skip || g.z == nil {
		return g.w.Write(b)
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
	g.pool.Put(g.z)
	g.z = nil
}

// acceptsGzip returns true if the given HTTP request indicates that it will
// accept a gzipped response.
func acceptsGzip(r *http.Request) bool {
	return allowsGzip(r.Header.Get(hdrAcceptEncoding))
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
				return q > 0
			}
			return false
		}
		return false
	}
	return false
}

func supportedContentType(s string) bool {
	switch s {
	case "":
		return false
	case "image/svg+xml", "font/woff", "font/woff2":
		return true
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

type writerPool interface {
	Get() *gzip.Writer
	Put(*gzip.Writer)
}

func newWriterPool(level int) writerPool {
	return &pool{
		sync.Pool{
			New: func() interface{} {
				w, err := gzip.NewWriterLevel(io.Discard, level)
				if err != nil {
					panic(err)
				}
				return w
			},
		},
	}
}

type pool struct {
	sync.Pool
}

func (p *pool) Get() *gzip.Writer  { return p.Pool.Get().(*gzip.Writer) }
func (p *pool) Put(w *gzip.Writer) { p.Pool.Put(w) }
