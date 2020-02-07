package diskcache

import (
	"bufio"
	"bytes"
	"compress/gzip"
	"compress/zlib"
	"fmt"
	"io"
	"io/ioutil"
	"net/http"
	"net/http/httputil"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/spf13/afero"
	"github.com/tdewolff/minify"
	"github.com/tdewolff/minify/css"
	"github.com/tdewolff/minify/html"
	"github.com/tdewolff/minify/js"
	"github.com/tdewolff/minify/json"
	"github.com/tdewolff/minify/svg"
	"github.com/tdewolff/minify/xml"
	"github.com/yookoala/realpath"
)

// Matcher is the shared interface for rewriting URLs to disk paths.
type Matcher interface {
	// Match matches the passed request, returning the key and ttl.
	Match(*http.Request) (string, time.Duration, error)
}

// Minifier is the shared interface for minifiers.
type Minifier interface {
	// Minify minifies from r to w, for the provided url and content type.
	Minify(w io.Writer, r io.Reader, urlstr string, code int, contentType string) error
}

// CompressDecompressor is the shared interface for compression
// implementations.
type CompressDecompressor interface {
	Compress(w io.Writer, r io.Reader) error
	Decompress(w io.Writer, r io.Reader) error
}

// Cache is a http.RoundTripper compatible disk cache.
type Cache struct {
	transport            http.RoundTripper
	fs                   afero.Fs
	matchers             []Matcher
	stripHeaders         []string
	minifier             Minifier
	compressDecompressor CompressDecompressor
}

// New creates a new disk cache.
//
// By default, the cache path will be <working directory>/cache. Change
// location using Options.
func New(opts ...Option) (*Cache, error) {
	c := new(Cache)
	for _, o := range opts {
		o(c)
	}

	// set default fs as overlay at <working directory>/cache
	if c.fs == nil {
		dir, err := os.Getwd()
		if err != nil {
			return nil, err
		}
		// ensure cacheDir exists and is directory
		cacheDir := filepath.Join(dir, "cache")
		fi, err := os.Stat(cacheDir)
		switch {
		case err != nil && os.IsNotExist(err):
			if err = os.MkdirAll(cacheDir, 0755); err != nil {
				return nil, err
			}
		case err != nil:
			return nil, err
		case err == nil && !fi.IsDir():
			return nil, fmt.Errorf("%s is not a directory", cacheDir)
		}
		// resolve real path
		cacheDir, err = realpath.Realpath(cacheDir)
		if err != nil {
			return nil, err
		}
		c.fs = afero.NewBasePathFs(afero.NewOsFs(), cacheDir)
	}

	return c, nil
}

// RoundTrip satisifies the http.RoundTripper interface.
func (c *Cache) RoundTrip(req *http.Request) (*http.Response, error) {
	key, ttl, err := c.Match(req)
	if err != nil {
		return nil, err
	}

	var stale bool
	fi, err := c.fs.Stat(key)
	switch {
	case err != nil && os.IsNotExist(err):
		stale = true
	case err != nil:
		return nil, err
	case fi.IsDir():
		return nil, fmt.Errorf("fs path %q is a directory", key)
	default:
		if ttl != 0 && time.Now().After(fi.ModTime().Add(ttl)) {
			stale = true
		}
	}

	if stale {
		transport := c.transport
		if transport == nil {
			transport = http.DefaultTransport
		}

		// grab
		res, err := transport.RoundTrip(req)
		if err != nil {
			return nil, err
		}
		defer res.Body.Close()

		// dump and process headers
		buf, err := httputil.DumpResponse(res, false)
		if err != nil {
			return nil, err
		}
		for _, h := range append(c.stripHeaders, "Transfer-Encoding") {
			re := regexp.MustCompile(`(?i)\r\n` + h + `:.+?\r\n`)
			for re.Match(buf) {
				buf = re.ReplaceAll(buf, []byte{'\r', '\n'})
			}
		}

		// minify body
		if c.minifier != nil {
			m := new(bytes.Buffer)
			if err = c.minifier.Minify(m, res.Body, req.URL.String(), res.StatusCode, res.Header.Get("Content-Type")); err != nil {
				return nil, err
			}
			buf = append(buf, m.Bytes()...)
		} else {
			body, err := ioutil.ReadAll(res.Body)
			if err != nil {
				return nil, err
			}
			buf = append(buf, body...)
		}

		// ensure path exists
		if err = c.fs.MkdirAll(path.Dir(key), 0755); err != nil {
			return nil, err
		}

		// open cache file
		f, err := c.fs.OpenFile(key, os.O_APPEND|os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0644)
		if err != nil {
			return nil, err
		}

		// compress
		if c.compressDecompressor != nil {
			b := new(bytes.Buffer)
			if err = c.compressDecompressor.Compress(b, bytes.NewReader(buf)); err != nil {
				return nil, err
			}
			buf = b.Bytes()
		}

		// store
		if _, err = f.Write(buf); err != nil {
			return nil, err
		}
		if err = f.Close(); err != nil {
			return nil, err
		}
	}

	// load
	var r io.Reader
	r, err = c.fs.OpenFile(key, os.O_RDONLY, 0)
	if err != nil {
		return nil, err
	}

	// decompress
	if c.compressDecompressor != nil {
		b := new(bytes.Buffer)
		if err = c.compressDecompressor.Decompress(b, r); err != nil {
			return nil, err
		}
		r = b
	}

	return http.ReadResponse(bufio.NewReader(r), req)
}

// Match finds the first matching cache policy for the request.
func (c *Cache) Match(req *http.Request) (string, time.Duration, error) {
	for _, m := range c.matchers {
		key, ttl, err := m.Match(req)
		if err != nil {
			return "", 0, err
		}
		if key != "" {
			return key, ttl, nil
		}
	}
	return "", 0, nil
}

// Evict forces a cache eviction (deletion) for the key matching the request.
func (c *Cache) Evict(req *http.Request) error {
	key, _, err := c.Match(req)
	if err != nil {
		return err
	}
	return c.EvictKey(key)
}

// Evict forces a cache eviction (deletion) of the specified key.
func (c *Cache) EvictKey(key string) error {
	return c.fs.Remove(key)
}

// Option is a disk cache option.
type Option func(*Cache)

// WithTransport is a disk cache option to set the underlying HTTP transport.
func WithTransport(transport http.RoundTripper) Option {
	return func(c *Cache) {
		c.transport = transport
	}
}

// WithFs is a disk cache option to set the Afero filesystem used.
//
// See: github.com/spf13/afero
func WithFs(fs afero.Fs) Option {
	return func(c *Cache) {
		c.fs = fs
	}
}

// WithMatchers is a disk cache option to set the matchers used.
func WithMatchers(matchers ...Matcher) Option {
	return func(c *Cache) {
		c.matchers = matchers
	}
}

// WithStripHeaders is a disk cache option to set HTTP response headers that
// are removed from a response prior to storage on disk.
//
// Useful for mangling HTTP responses by removing cookies, caching policies,
// etc.
func WithStripHeaders(stripHeaders ...string) Option {
	return func(c *Cache) {
		c.stripHeaders = stripHeaders
	}
}

// Minifier is a disk cache option to set a minifier.
//
// See: github.com/tdewolff/minify/http for out-of-the box compatible http minifier.
func WithMinifier(minifier Minifier) Option {
	return func(c *Cache) {
		c.minifier = minifier
	}
}

// WithBasicMinifier is a disk cache option to use a basic HTML minifier.
//
// If truncate is true, will drop the returned bodies for any non HTTP 200 OK
// responses.
//
// Builds a basic text/html minifier using github.com/tdewolff/minify.
func WithBasicMinifier(truncate bool) Option {
	return func(c *Cache) {
		c.minifier = BasicMinifier{truncate: truncate}
	}
}

// WithCompressDecompressor is a disk cache option to set a compression
// handler.
func WithCompressDecompressor(compressDecompressor CompressDecompressor) Option {
	return func(c *Cache) {
		c.compressDecompressor = compressDecompressor
	}
}

// WithGzipCompression is a disk cache option that stores/retrieves using gzip
// compression.
func WithGzipCompression() Option {
	return func(c *Cache) {
		c.compressDecompressor = GzipCompressDecompressor{Level: gzip.DefaultCompression}
	}
}

// WithZlibCompression is a disk cache option that stores/retrieves using zlib
// compression.
func WithZlibCompression() Option {
	return func(c *Cache) {
		c.compressDecompressor = ZlibCompressDecompressor{Level: zlib.DefaultCompression}
	}
}

// BasicMinifier is a basic html minifier.
type BasicMinifier struct {
	truncate bool
}

// Minify satisfies the Minifier interface.
func (m BasicMinifier) Minify(w io.Writer, r io.Reader, urlstr string, code int, contentType string) error {
	if m.truncate && code != http.StatusOK {
		return nil
	}

	if i := strings.Index(contentType, ";"); i != -1 {
		contentType = contentType[:i]
	}

	switch contentType {
	case "text/html":
		u, err := url.Parse(urlstr)
		if err != nil {
			return err
		}
		m := minify.New()
		m.AddFunc("text/html", html.Minify)
		m.AddFunc("text/css", css.Minify)
		m.AddFunc("image/svg+xml", svg.Minify)
		m.AddFuncRegexp(regexp.MustCompile("^(application|text)/(x-)?(java|ecma)script$"), js.Minify)
		m.AddFuncRegexp(regexp.MustCompile("[/+]json$"), json.Minify)
		m.AddFuncRegexp(regexp.MustCompile("[/+]xml$"), xml.Minify)
		m.URL = u
		return m.Minify("text/html", w, r)
	}

	_, err := io.Copy(w, r)
	if err != nil {
		return err
	}
	return nil
}

// GzipCompressDecompressor is a gzip compressor/decompressor.
type GzipCompressDecompressor struct {
	// Level is the compression level.
	Level int
}

// Compress satisfies the CompressDecompressor interface.
func (z GzipCompressDecompressor) Compress(w io.Writer, r io.Reader) error {
	c, err := gzip.NewWriterLevel(w, z.Level)
	if err != nil {
		return err
	}
	_, err = io.Copy(c, r)
	if err != nil {
		return err
	}
	if err = c.Flush(); err != nil {
		return err
	}
	return c.Close()
}

// Decompress satisfies the CompressDecompressor interface.
func (z GzipCompressDecompressor) Decompress(w io.Writer, r io.Reader) error {
	d, err := gzip.NewReader(r)
	if err != nil {
		return err
	}
	_, err = io.Copy(w, d)
	return err
}

// ZlibCompressDecompressor is a zlib compressor/decompressor.
type ZlibCompressDecompressor struct {
	// Level is the compression level.
	Level int

	// Dict is the compression dictionary.
	Dict []byte
}

// Compress satisfies the CompressDecompressor interface.
func (z ZlibCompressDecompressor) Compress(w io.Writer, r io.Reader) error {
	c, err := zlib.NewWriterLevelDict(w, z.Level, z.Dict)
	if err != nil {
		return err
	}
	_, err = io.Copy(c, r)
	return err
}

// Decompress satisfies the CompressDecompressor interface.
func (z ZlibCompressDecompressor) Decompress(w io.Writer, r io.Reader) error {
	d, err := zlib.NewReaderDict(r, z.Dict)
	if err != nil {
		return err
	}
	_, err = io.Copy(w, d)
	return err
}
