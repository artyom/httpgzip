package httpgzip

import (
	"compress/gzip"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
)

const hello = "Hello, world!\n"

func TestExplicitStatusCode(t *testing.T) {
	content := strings.Repeat(hello, compressThreshold/len(hello)+1)
	handler := New(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(content))
	}))
	t.Run("gzipped", testFunc(handler, true, true, content))
	t.Run("non-gzipped", testFunc(handler, false, false, content))
}

func TestImplicitContentType(t *testing.T) {
	content := strings.Repeat(hello, compressThreshold/len(hello)+1)
	handler := New(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(content))
		if f, ok := w.(http.Flusher); ok {
			f.Flush()
		}
	}))
	t.Run("gzipped", testFunc(handler, true, true, content))
	t.Run("non-gzipped", testFunc(handler, false, false, content))
}

func TestExplicitContentType(t *testing.T) {
	content := strings.Repeat(hello, compressThreshold/len(hello)+1)
	handler := New(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain")
		w.Write([]byte(content))
	}))
	t.Run("gzipped", testFunc(handler, true, true, content))
	t.Run("non-gzipped", testFunc(handler, false, false, content))
}

func TestSkipOnStatusCode(t *testing.T) {
	content := strings.Repeat(hello, compressThreshold/len(hello)+1)
	handler := New(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain")
		w.WriteHeader(http.StatusPartialContent)
		w.Write([]byte(content))
	}))
	t.Run("gzipped", testFunc(handler, true, false, content))
	t.Run("non-gzipped", testFunc(handler, false, false, content))
}

func TestSkipOnContentType(t *testing.T) {
	content := strings.Repeat(hello, compressThreshold/len(hello)+1)
	handler := New(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/octet-stream")
		w.Write([]byte(content))
	}))
	t.Run("gzipped", testFunc(handler, true, false, content))
	t.Run("non-gzipped", testFunc(handler, false, false, content))
}

func TestSkipOnHeader(t *testing.T) {
	content := strings.Repeat(hello, compressThreshold/len(hello)+1)
	handler := New(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set(hdrContentRange, "bytes 21010-47021/47022")
		w.Write([]byte(content))
	}))
	t.Run("gzipped", testFunc(handler, true, false, content))
	t.Run("non-gzipped", testFunc(handler, false, false, content))
}

func TestSkipOnSizeThreshold(t *testing.T) {
	content := strings.Repeat(hello, compressThreshold/len(hello)-1)
	handler := New(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Length", strconv.Itoa(len(content)))
		w.Write([]byte(content))
	}))
	t.Run("gzipped", testFunc(handler, true, false, content))
	t.Run("non-gzipped", testFunc(handler, false, false, content))
}

func testFunc(h http.Handler, acceptGzip, expectGzip bool, want string) func(*testing.T) {
	return func(t *testing.T) {
		t.Parallel()
		r := httptest.NewRequest(http.MethodGet, "/", nil)
		if acceptGzip {
			r.Header.Set(hdrAcceptEncoding, "gzip")
		}
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		result := w.Result()
		var data []byte
		var err error
		switch {
		case expectGzip:
			if ce := result.Header.Get("Content-Encoding"); ce != "gzip" {
				t.Fatalf("want Content-Encoding: gzip, got %q", ce)
			}
			data, err = readAllGzipped(w.Body)
		default:
			if ce := result.Header.Get("Content-Encoding"); ce != "" {
				t.Fatalf("want empty Content-Encoding, got %q", ce)
			}
			data, err = io.ReadAll(w.Body)
		}
		if err != nil {
			t.Fatal(err)
		}
		if string(data) != want {
			t.Fatal("read content differs from served")
		}
	}
}

func readAllGzipped(r io.Reader) ([]byte, error) {
	rd, err := gzip.NewReader(r)
	if err != nil {
		return nil, err
	}
	defer rd.Close()
	return io.ReadAll(rd)
}

func TestAllowsGzip(t *testing.T) {
	examples := []struct {
		hdr  string
		want bool
	}{
		// Examples from RFC 2616
		{"compress, gzip", true},
		{"", false},
		{"*", false},
		{"compress;q=0.5, gzip;q=1.0", true},
		{"gzip;q=1.0, identity; q=0.5, *;q=0", true},

		// More random stuff
		{"gzip;BAD, *q;q=0", false},
		{"gzip; q=X, *q;q=0", false},
		{"gzip;q=0.0, *;q=0", false},
		{"fgzip", false},
		{"AAA;q=1", false},
		{"BBB ; q = 2", false},
	}
	for n, ex := range examples {
		if got := allowsGzip(ex.hdr); got != ex.want {
			t.Fatalf("[%d] %q: got %v, want %v", n, ex.hdr, got, ex.want)
		}
	}
}

func Test_gRWUnwrap(t *testing.T) {
	t.Parallel()
	type rwUnwrapper interface {
		Unwrap() http.ResponseWriter
	}
	content := strings.Repeat(hello, compressThreshold/len(hello)+1)
	h := New(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if uw, ok := w.(rwUnwrapper); !ok {
			t.Error("ResponseWriter does not implement rwUnwrapper")
		} else {
			parent := uw.Unwrap()
			if _, ok := parent.(*httptest.ResponseRecorder); !ok {
				t.Errorf("parent ResponseWriter (%T) is not a *httptest.ResponseRecorder", parent)
			}
		}
		w.Header().Set("Content-Length", strconv.Itoa(len(content)))
		w.Write([]byte(content))
	}))
	r := httptest.NewRequest(http.MethodGet, "/", nil)
	r.Header.Set(hdrAcceptEncoding, "gzip")
	h.ServeHTTP(httptest.NewRecorder(), r)
}

func TestWithLevel(t *testing.T) {
	t.Parallel()
	fn := func(t *testing.T, level int, shouldPanic bool) {
		defer func() {
			if shouldPanic {
				if recover() == nil {
					t.Error("expected call to panic, but doesn't")
				}
			} else {
				if e := recover(); e != nil {
					t.Errorf("the call should not panic, but it does: %v", e)
				}
			}
		}()
		_ = WithLevel(level)
	}
	t.Run("bad#1", func(t *testing.T) { fn(t, gzip.HuffmanOnly-1, true) })
	t.Run("bad#2", func(t *testing.T) { fn(t, gzip.BestCompression+1, true) })
	t.Run("good#1", func(t *testing.T) { fn(t, gzip.HuffmanOnly, false) })
	t.Run("good#2", func(t *testing.T) { fn(t, gzip.BestCompression, false) })
}
