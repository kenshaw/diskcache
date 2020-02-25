package diskcache

import (
	"compress/gzip"
	"compress/zlib"
	"encoding/base64"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"time"

	"github.com/spf13/afero"
	"github.com/yookoala/realpath"
)

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
	t := Minifier{
		Priority: TransformPriorityMinify,
	}
	return option{
		c: func(c *Cache) error {
			c.matcher.policy.BodyTransformers = append(c.matcher.policy.BodyTransformers, t)
			return nil
		},
		m: func(m *SimpleMatcher) {
			m.policy.BodyTransformers = append(m.policy.BodyTransformers, t)
		},
	}
}

// WithTruncator is a disk cache option to add a body transformer that
// truncates responses based on match criteria.
func WithTrucator(priority TransformPriority, match func(string, int, string) bool) Option {
	t := Truncator{
		Priority: priority,
		Match:    match,
	}
	return option{
		c: func(c *Cache) error {
			c.matcher.policy.BodyTransformers = append(c.matcher.policy.BodyTransformers, t)
			return nil
		},
		m: func(m *SimpleMatcher) {
			m.policy.BodyTransformers = append(m.policy.BodyTransformers, t)
		},
	}
}

// WithStatusCodeTrunactor is a disk cache option to add a body transformer
// that truncates responses when the status code is not in the provided list.
func WithStatusCodeTruncator(codes ...int) Option {
	t := Truncator{
		Priority: TransformPriorityFirst,
		Match: func(_ string, code int, _ string) bool {
			return !intcontains(codes, code)
		},
	}
	return option{
		c: func(c *Cache) error {
			c.matcher.policy.BodyTransformers = append(c.matcher.policy.BodyTransformers, t)
			return nil
		},
		m: func(m *SimpleMatcher) {
			m.policy.BodyTransformers = append(m.policy.BodyTransformers, t)
		},
	}
}

// WithErrorTruncator is a disk cache option to add a body transformer that
// truncates responses when the HTTP status code != OK (200).
func WithErrorTruncator() Option {
	t := Truncator{
		Priority: TransformPriorityFirst,
		Match: func(_ string, code int, _ string) bool {
			return code != http.StatusOK
		},
	}
	return option{
		c: func(c *Cache) error {
			c.matcher.policy.BodyTransformers = append(c.matcher.policy.BodyTransformers, t)
			return nil
		},
		m: func(m *SimpleMatcher) {
			m.policy.BodyTransformers = append(m.policy.BodyTransformers, t)
		},
	}
}

// WithBase64Decoder is a disk cache option to add a body transformer that does
// base64 decoding of responses for specific content types.
func WithBase64Decoder(contentTypes ...string) Option {
	t := Base64Decoder{
		Priority:     TransformPriorityDecode,
		ContentTypes: contentTypes,
		Encoding:     base64.StdEncoding,
	}
	return option{
		c: func(c *Cache) error {
			c.matcher.policy.BodyTransformers = append(c.matcher.policy.BodyTransformers, t)
			return nil
		},
		m: func(m *SimpleMatcher) {
			m.policy.BodyTransformers = append(m.policy.BodyTransformers, t)
		},
	}
}

// WithPrefixStripper is a disk cache option to add a body transformer that
// strips a specific XSS prefix for a specified content type.
//
// Useful for removing XSS prefixes added to JavaScript or JSON content.
func WithPrefixStripper(prefix []byte, contentTypes ...string) Option {
	t := PrefixStripper{
		Priority:     TransformPriorityModify,
		ContentTypes: contentTypes,
		Prefix:       prefix,
	}
	return option{
		c: func(c *Cache) error {
			c.matcher.policy.BodyTransformers = append(c.matcher.policy.BodyTransformers, t)
			return nil
		},
		m: func(m *SimpleMatcher) {
			m.policy.BodyTransformers = append(m.policy.BodyTransformers, t)
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

// WithLongPathHandler is a disk cache option to set a long path handler.
func WithLongPathHandler(longPathHandler func(string) string) Option {
	return option{
		c: func(c *Cache) error {
			c.matcher.longPathHandler = longPathHandler
			return nil
		},
		m: func(m *SimpleMatcher) {
			m.longPathHandler = longPathHandler
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
