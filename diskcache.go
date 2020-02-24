// Package diskcache provides a http.RoundTripper implementation that can
// minify, compress, and cache HTTP responses retrieved using a standard
// http.Client on disk. Provides ability to define custom retention and storage
// policies depending on the host, path, or other URL components.
//
// Package diskcache does not aim to work as a on-disk HTTP proxy -- see
// github.com/gregjones/httpcache for a HTTP transport (http.RoundTripper)
// implementation that provides a RFC 7234 compliant cache.
//
// See _example/example.go for a more complete example.
package diskcache

import (
	"bufio"
	"bytes"
	"compress/gzip"
	"compress/zlib"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httputil"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"sort"
	"time"

	"github.com/spf13/afero"
	"github.com/yookoala/realpath"
)

// Policy is a disk cache policy.
type Policy struct {
	// TTL is the time-to-live.
	TTL time.Duration

	// HeaderTransformers are the set of header transformers.
	HeaderTransformers []HeaderTransformer

	// BodyTransformers are the set of body tranformers.
	BodyTransformers []BodyTransformer

	// MarshalUnmarshaler is the marshal/unmarshaler responsible for storage on
	// disk.
	MarshalUnmarshaler MarshalUnmarshaler
}

// Cache is a http.RoundTripper compatible disk cache.
type Cache struct {
	transport http.RoundTripper
	dirMode   os.FileMode
	fileMode  os.FileMode
	fs        afero.Fs
	noDefault bool

	// matchers are the set of url matchers.
	matchers []Matcher

	// matcher is default matcher.
	matcher *SimpleMatcher
}

// New creates a new disk cache.
//
// By default, the cache path will be <working directory>/cache. Change
// location using options.
func New(opts ...Option) (*Cache, error) {
	c := &Cache{
		dirMode:  0755,
		fileMode: 0644,
		matcher: Match(
			`GET`,
			`^(?P<proto>https?)://(?P<host>[^:]+)(:[0-9]+)?$`,
			`^/?(?P<path>.*)$`,
			`{{proto}}/{{host}}/{{path}}{{query}}`,
			WithIndexPath("?index"),
			WithQueryPrefix("_"),
		),
	}
	for _, o := range opts {
		if err := o.cache(c); err != nil {
			return nil, err
		}
	}

	// set default fs as overlay at <working directory>/cache
	if c.fs == nil {
		dir, err := os.Getwd()
		if err != nil {
			return nil, err
		}
		if err = WithBasePathFs(filepath.Join(dir, "cache")).cache(c); err != nil {
			return nil, err
		}
	}

	// ensure body transformers are in order.
	for _, v := range append(c.matchers, c.matcher) {
		m, ok := v.(*SimpleMatcher)
		if !ok {
			continue
		}
		sort.Slice(m.policy.BodyTransformers, func(a, b int) bool {
			return m.policy.BodyTransformers[a].TransformPriority() < m.policy.BodyTransformers[b].TransformPriority()
		})
	}

	return c, nil
}

// RoundTrip satisfies the http.RoundTripper interface.
func (c *Cache) RoundTrip(req *http.Request) (*http.Response, error) {
	key, p, err := c.Match(req)
	if err != nil {
		return nil, err
	}

	// no caching policy, pass to regular transport
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
		if p.TTL != 0 && time.Now().After(fi.ModTime().Add(p.TTL)) {
			stale = true
		}
	}

	var r *bufio.Reader
	if stale {
		r, err = c.do(key, p, req)
	} else {
		r, err = c.load(key, p)
	}
	if err != nil {
		return nil, err
	}
	return http.ReadResponse(r, req)
}

// do executes the request, applying header and body transformers, before
// marshaling and storing the response.
func (c *Cache) do(key string, p Policy, req *http.Request) (*bufio.Reader, error) {
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

	// dump and apply header tranforms
	buf, err := httputil.DumpResponse(res, false)
	if err != nil {
		return nil, err
	}
	buf = stripTransferEncodingHeader(buf)
	for _, hr := range p.HeaderTransformers {
		buf = hr.HeaderTransform(buf)
	}

	// apply body transforms
	buf, err = transformAndAppend(buf, res.Body, req.URL.String(), res.StatusCode, res.Header.Get("Content-Type"), p.BodyTransformers...)
	if err != nil {
		return nil, err
	}
	body := buf

	// marshal
	if p.MarshalUnmarshaler != nil {
		b := new(bytes.Buffer)
		if err = p.MarshalUnmarshaler.Marshal(b, bytes.NewReader(buf)); err != nil {
			return nil, err
		}
		buf = b.Bytes()
	}

	// store
	if len(buf) != 0 {
		// ensure path exists
		if err = c.fs.MkdirAll(path.Dir(key), c.dirMode); err != nil {
			return nil, err
		}
		// open cache file
		f, err := c.fs.OpenFile(key, os.O_APPEND|os.O_CREATE|os.O_WRONLY|os.O_TRUNC, c.fileMode)
		if err != nil {
			return nil, err
		}
		if _, err = f.Write(buf); err != nil {
			return nil, err
		}
		if err = f.Close(); err != nil {
			return nil, err
		}
	}
	return bufio.NewReader(bytes.NewReader(body)), nil
}

// load reads the stored data on disk and unmarshals the response.
func (c *Cache) load(key string, p Policy) (*bufio.Reader, error) {
	var err error
	var r io.Reader
	r, err = c.fs.OpenFile(key, os.O_RDONLY, 0)
	if err != nil {
		return nil, err
	}
	if p.MarshalUnmarshaler != nil {
		b := new(bytes.Buffer)
		if err = p.MarshalUnmarshaler.Unmarshal(b, r); err != nil {
			return nil, err
		}
		r = b
	}
	return bufio.NewReader(r), nil
}

// Match finds the first matching cache policy for the request.
func (c *Cache) Match(req *http.Request) (string, Policy, error) {
	matchers := c.matchers
	if !c.noDefault {
		matchers = append(matchers, c.matcher)
	}
	for _, m := range matchers {
		key, p, err := m.Match(req)
		if err != nil {
			return "", Policy{}, err
		}
		if key != "" {
			return key, p, nil
		}
	}
	return "", Policy{}, nil
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
type Option interface {
	cache(*Cache) error
	simpleMatcher(*SimpleMatcher)
}

// option wraps setting disk cache and simple matcher options.
type option struct {
	c func(*Cache) error
	m func(*SimpleMatcher)
}

// cache satisifies the Option interface.
func (v option) cache(c *Cache) error {
	if v.c == nil {
		return errors.New("option not available for disk cache")
	}
	return v.c(c)
}

// simpleMatcher satisfies the Option interface.
func (v option) simpleMatcher(m *SimpleMatcher) {
	if v.m == nil {
		panic(fmt.Sprintf("option not available for simple matcher"))
	}
	v.m(m)
}

// WithTransport is a disk cache option to set the underlying HTTP transport.
func WithTransport(transport http.RoundTripper) Option {
	return option{
		c: func(c *Cache) error {
			c.transport = transport
			return nil
		},
	}
}

// WithMode is a disk cache option to set the file mode used when creating
// files and directories on disk.
func WithMode(dirMode, fileMode os.FileMode) Option {
	return option{
		c: func(c *Cache) error {
			c.dirMode, c.fileMode = dirMode, fileMode
			return nil
		},
	}
}

// WithFs is a disk cache option to set the Afero filesystem used.
//
// See: github.com/spf13/afero
func WithFs(fs afero.Fs) Option {
	return option{
		c: func(c *Cache) error {
			c.fs = fs
			return nil
		},
	}
}

// WithBasePathFs is a disk cache option to set the Afero filesystem as an
// Afero BasePathFs.
func WithBasePathFs(basePath string) Option {
	return option{
		c: func(c *Cache) error {
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
		},
	}
}

// WithMatchers is a disk cache option to set matchers.
func WithMatchers(matchers ...Matcher) Option {
	return option{
		c: func(c *Cache) error {
			c.matchers = matchers
			return nil
		},
	}
}

// WithDefaultMatcher is a disk cache option to set the default matcher.
func WithDefaultMatcher(method, host, path, key string, opts ...Option) Option {
	m := Match(method, host, path, key, opts...)
	return option{
		c: func(c *Cache) error {
			x := &Cache{noDefault: true}
			if err := m.cache(x); err != nil {
				return err
			}
			c.matcher = x.matchers[0].(*SimpleMatcher)
			return nil
		},
	}
}

// WithNoDefault is a disk cache option to disable default matcher.
//
// Prevents propagating settings from default matcher.
func WithNoDefault() Option {
	return option{
		c: func(c *Cache) error {
			c.noDefault = true
			return nil
		},
	}
}

// WithHeaderTransformers is a disk cache option to set the header
// transformers.
func WithHeaderTransformers(headerTransformers ...HeaderTransformer) Option {
	return option{
		c: func(c *Cache) error {
			c.matcher.policy.HeaderTransformers = headerTransformers
			return nil
		},
		m: func(m *SimpleMatcher) {
			m.policy.HeaderTransformers = headerTransformers
		},
	}
}

// WithHeaderBlacklist is a disk cache option to add a header transformer that
// removes any header in the blacklist.
func WithHeaderBlacklist(blacklist ...string) Option {
	headerTransformer, err := stripHeaders(blacklist...)
	if err != nil {
		panic(err)
	}
	return option{
		c: func(c *Cache) error {
			c.matcher.policy.HeaderTransformers = append(c.matcher.policy.HeaderTransformers, headerTransformer)
			return nil
		},
		m: func(m *SimpleMatcher) {
			m.policy.HeaderTransformers = append(m.policy.HeaderTransformers, headerTransformer)
		},
	}
}

// WithHeaderWhitelist is a disk cache option to add a header transformer that
// removes any header not in the whitelist.
func WithHeaderWhitelist(whitelist ...string) Option {
	headerTransformer, err := keepHeaders(whitelist...)
	if err != nil {
		panic(err)
	}
	return option{
		c: func(c *Cache) error {
			c.matcher.policy.HeaderTransformers = append(c.matcher.policy.HeaderTransformers, headerTransformer)
			return nil
		},
		m: func(m *SimpleMatcher) {
			m.policy.HeaderTransformers = append(m.policy.HeaderTransformers, headerTransformer)
		},
	}
}

// WithHeaderTransform is a disk cache option to add a header transformer that
// transforms headers matching the provided regexp pairs and replacements.
func WithHeaderTransform(pairs ...string) Option {
	headerTransformer, err := NewHeaderTransformer(pairs...)
	if err != nil {
		panic(err)
	}
	return option{
		c: func(c *Cache) error {
			c.matcher.policy.HeaderTransformers = append(c.matcher.policy.HeaderTransformers, headerTransformer)
			return nil
		},
		m: func(m *SimpleMatcher) {
			m.policy.HeaderTransformers = append(m.policy.HeaderTransformers, headerTransformer)
		},
	}
}

// WithBodyTransformers is a disk cache option to set the body transformers.
func WithBodyTransformers(bodyTransformers ...BodyTransformer) Option {
	return option{
		c: func(c *Cache) error {
			c.matcher.policy.BodyTransformers = bodyTransformers
			return nil
		},
		m: func(m *SimpleMatcher) {
			m.policy.BodyTransformers = bodyTransformers
		},
	}
}

// WithMinifier is a disk cache option to add a body transformer that does
// content minification of HTML, XML, SVG, JavaScript, JSON, and CSS data.
// Useful for reducing disk storage sizes.
//
// See: github.com/tdewolff/minify
func WithMinifier() Option {
	z := Minifier{
		Priority: TransformPriorityMinify,
	}
	return option{
		c: func(c *Cache) error {
			c.matcher.policy.BodyTransformers = append(c.matcher.policy.BodyTransformers, z)
			return nil
		},
		m: func(m *SimpleMatcher) {
			m.policy.BodyTransformers = append(m.policy.BodyTransformers, z)
		},
	}
}

// WithErrorTruncator is a disk cache option to add a body transformer that
// truncates responses when the HTTP status code != OK (200).
func WithErrorTruncator() Option {
	z := ErrorTruncator{
		Priority: TransformPriorityFirst,
	}
	return option{
		c: func(c *Cache) error {
			c.matcher.policy.BodyTransformers = append(c.matcher.policy.BodyTransformers, z)
			return nil
		},
		m: func(m *SimpleMatcher) {
			m.policy.BodyTransformers = append(m.policy.BodyTransformers, z)
		},
	}
}

// WithBase64Decoder is a disk cache option to add a body transformer that does
// base64 decoding of responses for specific content types.
func WithBase64Decoder(contentType string) Option {
	z := Base64Decoder{
		Priority:    TransformPriorityDecode,
		Encoding:    base64.StdEncoding,
		ContentType: contentType,
	}
	return option{
		c: func(c *Cache) error {
			c.matcher.policy.BodyTransformers = append(c.matcher.policy.BodyTransformers, z)
			return nil
		},
		m: func(m *SimpleMatcher) {
			m.policy.BodyTransformers = append(m.policy.BodyTransformers, z)
		},
	}
}

// WithPrefixStripper is a disk cache option to add a body transformer that
// strips a specific XSS prefix for a specified content type.
//
// Useful for removing XSS prefixes added to JavaScript or JSON content.
func WithPrefixStripper(prefix []byte, contentType string) Option {
	z := PrefixStripper{
		Priority:    TransformPriorityModify,
		Prefix:      prefix,
		ContentType: contentType,
	}
	return option{
		c: func(c *Cache) error {
			c.matcher.policy.BodyTransformers = append(c.matcher.policy.BodyTransformers, z)
			return nil
		},
		m: func(m *SimpleMatcher) {
			m.policy.BodyTransformers = append(m.policy.BodyTransformers, z)
		},
	}
}

// WithMarshalUnmarshaler is a disk cache option to set a marshaler/unmarshaler.
func WithMarshalUnmarshaler(marshalUnmarshaler MarshalUnmarshaler) Option {
	return option{
		c: func(c *Cache) error {
			c.matcher.policy.MarshalUnmarshaler = marshalUnmarshaler
			return nil
		},
		m: func(m *SimpleMatcher) {
			m.policy.MarshalUnmarshaler = marshalUnmarshaler
		},
	}
}

// WithGzipCompression is a disk cache option to set a gzip marshaler/unmarshaler.
func WithGzipCompression() Option {
	z := GzipMarshalUnmarshaler{
		Level: gzip.DefaultCompression,
	}
	return option{
		c: func(c *Cache) error {
			c.matcher.policy.MarshalUnmarshaler = z
			return nil
		},
		m: func(m *SimpleMatcher) {
			m.policy.MarshalUnmarshaler = z
		},
	}
}

// WithZlibCompression is a disk cache option to set a zlib marshaler/unmarshaler.
func WithZlibCompression() Option {
	z := ZlibMarshalUnmarshaler{
		Level: zlib.DefaultCompression,
	}
	return option{
		c: func(c *Cache) error {
			c.matcher.policy.MarshalUnmarshaler = z
			return nil
		},
		m: func(m *SimpleMatcher) {
			m.policy.MarshalUnmarshaler = z
		},
	}
}

// WithFlatStorage is a disk cache option to set a flat marshaler/unmarshaler
// removing headers from responses.
//
// Note: cached responses will not have original headers.
func WithFlatStorage() Option {
	z := FlatMarshalUnmarshaler{}
	return option{
		c: func(c *Cache) error {
			c.matcher.policy.MarshalUnmarshaler = z
			return nil
		},
		m: func(m *SimpleMatcher) {
			m.policy.MarshalUnmarshaler = z
		},
	}
}

// WithFlatChain is a disk cache option that marshals/unmarshals responses,
// removing headers from responses, and chaining marshaling/unmarshaling to a
// provided marshaler/unmarshaler.
//
// Note: cached responses will not have original headers.
func WithFlatChain(marshalUnmarshaler MarshalUnmarshaler) Option {
	z := FlatMarshalUnmarshaler{Chain: marshalUnmarshaler}
	return option{
		c: func(c *Cache) error {
			c.matcher.policy.MarshalUnmarshaler = z
			return nil
		},
		m: func(m *SimpleMatcher) {
			m.policy.MarshalUnmarshaler = z
		},
	}
}

// WithFlatGzipCompression is a disk cache option that marshals/unmarshals
// responses, with headers removed from responses, and with gzip compression.
//
// Note: cached responses will not have original headers.
func WithFlatGzipCompression() Option {
	return WithFlatChain(GzipMarshalUnmarshaler{
		Level: gzip.DefaultCompression,
	})
}

// WithFlatZlibCompression is a disk cache option that marshals/unmarshals
// responses, with headers removed from responses, and with zlib compression.
//
// Note: cached responses will not have original headers.
func WithFlatZlibCompression() Option {
	return WithFlatChain(ZlibMarshalUnmarshaler{
		Level: zlib.DefaultCompression,
	})
}

// WithTTL is a disk cache option to set the cache policy TTL.
func WithTTL(ttl time.Duration) Option {
	return option{
		c: func(c *Cache) error {
			c.matcher.policy.TTL = ttl
			return nil
		},
		m: func(m *SimpleMatcher) {
			m.policy.TTL = ttl
		},
	}
}

// WithIndexPath is a disk cache option to set the index path name.
func WithIndexPath(indexPath string) Option {
	return option{
		c: func(c *Cache) error {
			c.matcher.indexPath = indexPath
			return nil
		},
		m: func(m *SimpleMatcher) {
			m.indexPath = indexPath
		},
	}
}

// WithQueryEncoder is a disk cache option to set the query encoder.
func WithQueryEncoder(queryEncoder func(url.Values) string) Option {
	return option{
		c: func(c *Cache) error {
			c.matcher.queryEncoder = queryEncoder
			return nil
		},
		m: func(m *SimpleMatcher) {
			m.queryEncoder = queryEncoder
		},
	}
}

// WithQueryPrefix is a disk cache option that sets a query encoder, that adds
// the supplied prefix to non-empty and canonical encoding
//
// The query string encoder can be limited to only the passed fields.
func WithQueryPrefix(prefix string, fields ...string) Option {
	f := func(v url.Values) string {
		if len(fields) > 0 {
			for k := range v {
				if !contains(fields, k) {
					delete(v, k)
				}
			}
		}
		if s := url.QueryEscape(v.Encode()); s != "" {
			return prefix + s
		}
		return ""
	}
	return option{
		c: func(c *Cache) error {
			c.matcher.queryEncoder = f
			return nil
		},
		m: func(m *SimpleMatcher) {
			m.queryEncoder = f
		},
	}
}
