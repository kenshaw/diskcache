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
	dirMode              os.FileMode
	fileMode             os.FileMode
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
	c := &Cache{dirMode: 0755, fileMode: 0644}
	for _, o := range opts {
		if err := o(c); err != nil {
			return nil, err
		}
	}

	// set default fs as overlay at <working directory>/cache
	if c.fs == nil {
		dir, err := os.Getwd()
		if err != nil {
			return nil, err
		}
		if err = WithBasePathFs(filepath.Join(dir, "cache"))(c); err != nil {
			return nil, err
		}
	}

	return c, nil
}

// RoundTrip satisifies the http.RoundTripper interface.
func (c *Cache) RoundTrip(req *http.Request) (*http.Response, error) {
	key, ttl, err := c.Match(req)
	if err != nil {
		return nil, err
	}

	// no disk caching policy available
	if key == "" {
		transport := c.transport
		if transport == nil {
			transport = http.DefaultTransport
		}
		return transport.RoundTrip(req)
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
		if err = c.fs.MkdirAll(path.Dir(key), c.dirMode); err != nil {
			return nil, err
		}

		// open cache file
		f, err := c.fs.OpenFile(key, os.O_APPEND|os.O_CREATE|os.O_WRONLY|os.O_TRUNC, c.fileMode)
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
type Option func(*Cache) error

// WithTransport is a disk cache option to set the underlying HTTP transport.
func WithTransport(transport http.RoundTripper) Option {
	return func(c *Cache) error {
		c.transport = transport
		return nil
	}
}

// WithMode is a disk cache option to set the file mode used when creating
// files and directories on disk.
func WithMode(dirMode, fileMode os.FileMode) Option {
	return func(c *Cache) error {
		c.dirMode, c.fileMode = dirMode, fileMode
		return nil
	}
}

// WithFs is a disk cache option to set the Afero filesystem used.
//
// See: github.com/spf13/afero
func WithFs(fs afero.Fs) Option {
	return func(c *Cache) error {
		c.fs = fs
		return nil
	}
}

// WithBasePathFs is a disk cache option to set the Afero filesystem as an
// Afero BasePathFs.
func WithBasePathFs(basePath string) Option {
	return func(c *Cache) error {
		// ensure path exists and is directory
		fi, err := os.Stat(basePath)
		switch {
		case err != nil && os.IsNotExist(err):
			if err = os.MkdirAll(basePath, c.dirMode); err != nil {
				return err
			}
		case err != nil:
			return err
		case err == nil && !fi.IsDir():
			return fmt.Errorf("%s is not a directory", basePath)
		}

		// resolve real path
		basePath, err = realpath.Realpath(basePath)
		if err != nil {
			return err
		}
		c.fs = afero.NewBasePathFs(afero.NewOsFs(), basePath)
		return nil
	}
}

// WithMatchers is a disk cache option to set the matchers used.
func WithMatchers(matchers ...Matcher) Option {
	return func(c *Cache) error {
		c.matchers = matchers
		return nil
	}
}

// WithStripHeaders is a disk cache option to set HTTP response headers that
// are removed from a response prior to storage on disk.
//
// Useful for mangling HTTP responses by removing cookies, caching policies,
// etc.
func WithStripHeaders(stripHeaders ...string) Option {
	return func(c *Cache) error {
		c.stripHeaders = stripHeaders
		return nil
	}
}

// Minifier is a disk cache option to set a minifier.
//
// See: github.com/tdewolff/minify/http for out-of-the box compatible http minifier.
func WithMinifier(minifier Minifier) Option {
	return func(c *Cache) error {
		c.minifier = minifier
		return nil
	}
}

// WithBasicMinifier is a disk cache option to use a basic HTML minifier.
//
// If truncate is true, will drop the returned bodies for any non HTTP 200 OK
// responses.
//
// Builds a basic text/html minifier using github.com/tdewolff/minify.
func WithBasicMinifier(truncate bool) Option {
	return func(c *Cache) error {
		c.minifier = BasicMinifier{truncate: truncate}
		return nil
	}
}

// WithCompressDecompressor is a disk cache option to set a compression
// handler.
func WithCompressDecompressor(compressDecompressor CompressDecompressor) Option {
	return func(c *Cache) error {
		c.compressDecompressor = compressDecompressor
		return nil
	}
}

// WithGzipCompression is a disk cache option that stores/retrieves using gzip
// compression.
func WithGzipCompression() Option {
	return func(c *Cache) error {
		c.compressDecompressor = GzipCompressDecompressor{Level: gzip.DefaultCompression}
		return nil
	}
}

// WithZlibCompression is a disk cache option that stores/retrieves using zlib
// compression.
func WithZlibCompression() Option {
	return func(c *Cache) error {
		c.compressDecompressor = ZlibCompressDecompressor{Level: zlib.DefaultCompression}
		return nil
	}
}

// BasicMinifier is a basic html minifier.
type BasicMinifier struct {
	truncate bool
}

var (
	jsContentTypeRE   = regexp.MustCompile("^(application|text)/(x-)?(java|ecma)script$")
	jsonContentTypeRE = regexp.MustCompile("[/+]json$")
	xmlContentTypeRE  = regexp.MustCompile("[/+]xml$")
)

// Minify satisfies the Minifier interface.
func (m BasicMinifier) Minify(w io.Writer, r io.Reader, urlstr string, code int, contentType string) error {
	if m.truncate && code != http.StatusOK {
		return nil
	}

	if i := strings.Index(contentType, ";"); i != -1 {
		contentType = contentType[:i]
	}

	switch {
	default:
		_, err := io.Copy(w, r)
		if err != nil {
			return err
		}
		return nil

	case contentType == "text/html",
		contentType == "text/css",
		contentType == "image/svg+xml",
		jsContentTypeRE.MatchString(contentType),
		jsonContentTypeRE.MatchString(contentType),
		xmlContentTypeRE.MatchString(contentType):
	}

	z := minify.New()
	z.AddFunc("text/html", html.Minify)
	z.AddFunc("text/css", css.Minify)
	z.AddFunc("image/svg+xml", svg.Minify)
	z.AddFuncRegexp(jsContentTypeRE, js.Minify)
	z.AddFuncRegexp(jsonContentTypeRE, json.Minify)
	z.AddFuncRegexp(xmlContentTypeRE, xml.Minify)
	if contentType == "text/html" {
		var err error
		z.URL, err = url.Parse(urlstr)
		if err != nil {
			return err
		}
	}
	return z.Minify(contentType, w, r)
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
