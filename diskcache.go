// Package diskcache provides a http.RoundTripper implementation that can
// minify, compress, and cache URLs retrieved using a standard http.Client on
// disk based on custom matching, retention, and path rewriting rules.
//
// Package diskcache does not aim to work as a on-disk HTTP proxy -- see
// github.com/gregjones/httpcache for a different http.RoundTripper
// implementation that can act as a standard HTTP proxy.
//
// Please see _example/example.go for a complete example.
package diskcache

import (
	"bufio"
	"bytes"
	"compress/gzip"
	"compress/zlib"
	"encoding/base64"
	"fmt"
	"io"
	"io/ioutil"
	"net/http"
	"net/http/httputil"
	"os"
	"path"
	"path/filepath"
	"sort"
	"time"

	"github.com/spf13/afero"
	"github.com/yookoala/realpath"
)

// Cache is a http.RoundTripper compatible disk cache.
type Cache struct {
	transport            http.RoundTripper
	dirMode              os.FileMode
	fileMode             os.FileMode
	fs                   afero.Fs
	matchers             []Matcher
	headerMungers        []HeaderMunger
	bodyMungers          []BodyMunger
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

	// sort mungers
	sort.Slice(c.bodyMungers, func(a, b int) bool {
		return c.bodyMungers[a].MungePriority() < c.bodyMungers[b].MungePriority()
	})

	return c, nil
}

// RoundTrip satisfies the http.RoundTripper interface.
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
		buf = stripTransferEncodingHeader(buf)
		for _, hr := range c.headerMungers {
			buf = hr.HeaderMunge(buf)
		}

		// munge body
		buf, err = c.mungeAndAppend(buf, res.Body, req.URL.String(), res.StatusCode, res.Header.Get("Content-Type"))
		if err != nil {
			return nil, err
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

// mungeAndAppend walks the body munger chain, applying each successive body
// munger.
func (c *Cache) mungeAndAppend(buf []byte, r io.Reader, urlstr string, code int, contentType string) ([]byte, error) {
	if c.bodyMungers != nil {
		for _, m := range c.bodyMungers {
			w := new(bytes.Buffer)
			success, err := m.BodyMunge(w, r, urlstr, code, contentType)
			if err != nil {
				return nil, err
			}
			r = bytes.NewReader(w.Bytes())
			if !success {
				break
			}
		}
	}
	body, err := ioutil.ReadAll(r)
	if err != nil {
		return nil, err
	}
	return append(stripContentLengthHeader(buf), body...), nil
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
			return fmt.Errorf("base path %s is not a directory", basePath)
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

// WithHeaderMungers is a disk cache option to set the header rewriters used.
func WithHeaderMungers(headerMungers ...HeaderMunger) Option {
	return func(c *Cache) error {
		c.headerMungers = headerMungers
		return nil
	}
}

// WithHeaderBlacklist is a disk cache option to set a header rewriter that
// strips all headers in the blacklist.
func WithHeaderBlacklist(blacklist ...string) Option {
	return func(c *Cache) error {
		headerMunger, err := stripHeaders(blacklist...)
		if err != nil {
			return err
		}
		c.headerMungers = append(c.headerMungers, headerMunger)
		return nil
	}
}

// WithHeaderWhitelist is a disk cache option to set a header rewriter that
// strips any headers not in the whitelist.
func WithHeaderWhitelist(whitelist ...string) Option {
	return func(c *Cache) error {
		headerMunger, err := keepHeaders(whitelist...)
		if err != nil {
			return err
		}
		c.headerMungers = append(c.headerMungers, headerMunger)
		return nil
	}
}

// WithHeaderMunger is a disk cache option to set pairs of matching and
// replacement regexps that modify the header prior to storage on disk.
func WithHeaderMunger(pairs ...string) Option {
	return func(c *Cache) error {
		headerMunger, err := NewHeaderMunger(pairs...)
		if err != nil {
			return err
		}
		c.headerMungers = append(c.headerMungers, headerMunger)
		return nil
	}
}

// WithBodyMungers is a disk cache option to set the body mungers used.
func WithBodyMungers(bodyMungers ...BodyMunger) Option {
	return func(c *Cache) error {
		c.bodyMungers = c.bodyMungers
		return nil
	}
}

// WithMinifier is a disk cache option to add a body munger that does content
// minification of HTML, XML, SVG, JavaScript, JSON, and CSS data to reduce
// storage overhead.
//
// See: github.com/tdewolff/minify
func WithMinifier() Option {
	return func(c *Cache) error {
		c.bodyMungers = append(c.bodyMungers, Minifier{
			Priority: MungePriorityMinify,
		})
		return nil
	}
}

// WithErrorTruncator is a disk cache option to add a body munger that
// truncates the response when the HTTP status code != OK (200).
func WithErrorTruncator() Option {
	return func(c *Cache) error {
		c.bodyMungers = append(c.bodyMungers, ErrorTruncator{
			Priority: MungePriorityFirst,
		})
		return nil
	}
}

// WithBase64Decoder is a disk cache option to add a body munger that decodes
func WithBase64Decoder(contentType string) Option {
	return func(c *Cache) error {
		c.bodyMungers = append(c.bodyMungers, Base64Decoder{
			Priority:    MungePriorityDecode,
			Encoding:    base64.StdEncoding,
			ContentType: contentType,
		})
		return nil
	}
}

// WithPrefixStripper is a disk cache option to strip a specific XSS prefix for a
// specified content type.
//
// Useful for decoding JavaScript or JSON data that has add a XSS prevention
// prefixed to it (ie, ).
func WithPrefixStripper(prefix []byte, contentType string) Option {
	return func(c *Cache) error {
		c.bodyMungers = append(c.bodyMungers, PrefixStripper{
			Priority:    MungePriorityModify,
			Prefix:      prefix,
			ContentType: contentType,
		})
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
		c.compressDecompressor = GzipCompressDecompressor{
			Level: gzip.DefaultCompression,
		}
		return nil
	}
}

// WithZlibCompression is a disk cache option that stores/retrieves using zlib
// compression.
func WithZlibCompression() Option {
	return func(c *Cache) error {
		c.compressDecompressor = ZlibCompressDecompressor{
			Level: zlib.DefaultCompression,
		}
		return nil
	}
}
