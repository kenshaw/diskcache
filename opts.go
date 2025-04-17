package diskcache

import (
	"compress/gzip"
	"compress/zlib"
	"encoding/base64"
	"errors"
	"fmt"
	"io/fs"
	"net/http"
	"net/url"
	"os"
	"slices"
	"strings"
	"time"

	"github.com/gobwas/glob"
	"github.com/spf13/afero"
	"github.com/yookoala/realpath"
)

// Option is a disk cache option.
type Option interface {
	apply(any) error
}

// option wraps setting disk cache and simple matcher options.
type option struct {
	cache   func(*Cache) error
	matcher func(*SimpleMatcher) error
}

// apply satisfies the Option interface.
func (opt option) apply(v any) error {
	switch z := v.(type) {
	case *Cache:
		return opt.cache(z)
	case *SimpleMatcher:
		return opt.matcher(z)
	}
	return fmt.Errorf("option cannot be used with %T", v)
}

// WithMethod is a disk cache option to set matching request method(s).
func WithMethod(method ...string) Option {
	return option{
		cache: func(c *Cache) error {
			return WithMethod(method...).apply(c.matcher)
		},
		matcher: func(m *SimpleMatcher) error {
			var err error
			m.method, err = glob.Compile("{"+strings.Join(method, ",")+"}", ',')
			return err
		},
	}
}

// WithTransport is a disk cache option to set the underlying HTTP transport.
func WithTransport(transport http.RoundTripper) Option {
	return option{
		cache: func(c *Cache) error {
			c.transport = transport
			return nil
		},
	}
}

// WithMode is a disk cache option to set the file mode used when creating
// files and directories on disk.
func WithMode(dirMode, fileMode os.FileMode) Option {
	return option{
		cache: func(c *Cache) error {
			c.dirMode, c.fileMode = dirMode, fileMode
			return nil
		},
	}
}

// WithFs is a disk cache option to set the afero fs used.
//
// See: https://github.com/spf13/afero
func WithFs(fs afero.Fs) Option {
	return option{
		cache: func(c *Cache) error {
			c.fs = fs
			return nil
		},
	}
}

// WithBasePathFs is a disk cache option to set the afero fs used locked to a
// base directory.
//
// See: https://github.com/spf13/afero
func WithBasePathFs(basePath string) Option {
	return option{
		cache: func(c *Cache) error {
			// ensure path exists and is directory
			fi, err := os.Stat(basePath)
			switch {
			case err != nil && errors.Is(err, fs.ErrNotExist):
				if err := os.MkdirAll(basePath, c.dirMode); err != nil {
					return err
				}
			case err != nil:
				return err
			case !fi.IsDir():
				return fmt.Errorf("base path %s is not a directory", basePath)
			}
			// resolve real path
			if basePath, err = realpath.Realpath(basePath); err != nil {
				return err
			}
			c.fs = afero.NewBasePathFs(afero.NewOsFs(), basePath)
			return nil
		},
	}
}

// WithAppCacheDir is a disk cache option to set the afero fs used locked to
// the user's cache directory joined with the app name and any passed paths.
//
// The afero base fs directory will typically be $HOME/.cache/<app>/paths...
func WithAppCacheDir(app string, paths ...string) Option {
	return option{
		cache: func(c *Cache) error {
			dir, err := UserCacheDir(append([]string{app}, paths...)...)
			if err != nil {
				return err
			}
			return WithBasePathFs(dir).apply(c)
		},
	}
}

// WithMatchers is a disk cache option to set matchers.
func WithMatchers(matchers ...Matcher) Option {
	return option{
		cache: func(c *Cache) error {
			c.matchers = matchers
			return nil
		},
	}
}

// WithDefaultMatcher is a disk cache option to set the default matcher.
func WithDefaultMatcher(method, host, path, key string, opts ...Option) Option {
	return option{
		cache: func(c *Cache) error {
			m, err := NewSimpleMatcher(method, host, path, key, opts...)
			if err != nil {
				return err
			}
			x := &Cache{noDefault: true}
			if err := m.apply(x); err != nil {
				return err
			}
			c.matcher = x.matchers[0].(*SimpleMatcher)
			return nil
		},
	}
}

// WithNoDefault is a disk cache option to disable the default matcher.
//
// Prevents propagating settings from default matcher.
func WithNoDefault() Option {
	return option{
		cache: func(c *Cache) error {
			c.noDefault = true
			return nil
		},
	}
}

// WithHeaderTransformers is a disk cache option to set the header
// transformers.
func WithHeaderTransformers(headerTransformers ...HeaderTransformer) Option {
	return option{
		cache: func(c *Cache) error {
			c.matcher.policy.HeaderTransformers = headerTransformers
			return nil
		},
		matcher: func(m *SimpleMatcher) error {
			m.policy.HeaderTransformers = headerTransformers
			return nil
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
		cache: func(c *Cache) error {
			c.matcher.policy.HeaderTransformers = append(c.matcher.policy.HeaderTransformers, headerTransformer)
			return nil
		},
		matcher: func(m *SimpleMatcher) error {
			m.policy.HeaderTransformers = append(m.policy.HeaderTransformers, headerTransformer)
			return nil
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
		cache: func(c *Cache) error {
			c.matcher.policy.HeaderTransformers = append(c.matcher.policy.HeaderTransformers, headerTransformer)
			return nil
		},
		matcher: func(m *SimpleMatcher) error {
			m.policy.HeaderTransformers = append(m.policy.HeaderTransformers, headerTransformer)
			return nil
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
		cache: func(c *Cache) error {
			c.matcher.policy.HeaderTransformers = append(c.matcher.policy.HeaderTransformers, headerTransformer)
			return nil
		},
		matcher: func(m *SimpleMatcher) error {
			m.policy.HeaderTransformers = append(m.policy.HeaderTransformers, headerTransformer)
			return nil
		},
	}
}

// WithBodyTransformers is a disk cache option to set the body transformers.
func WithBodyTransformers(bodyTransformers ...BodyTransformer) Option {
	return option{
		cache: func(c *Cache) error {
			c.matcher.policy.BodyTransformers = bodyTransformers
			return nil
		},
		matcher: func(m *SimpleMatcher) error {
			m.policy.BodyTransformers = bodyTransformers
			return nil
		},
	}
}

// WithMinifier is a disk cache option to add a body transformer that does
// content minification of HTML, XML, SVG, JavaScript, JSON, and CSS data.
// Useful for reducing disk storage sizes.
//
// See: https://github.com/tdewolff/minify
func WithMinifier() Option {
	t := Minifier{
		Priority: TransformPriorityMinify,
	}
	return option{
		cache: func(c *Cache) error {
			c.matcher.policy.BodyTransformers = append(c.matcher.policy.BodyTransformers, t)
			return nil
		},
		matcher: func(m *SimpleMatcher) error {
			m.policy.BodyTransformers = append(m.policy.BodyTransformers, t)
			return nil
		},
	}
}

// WithTruncator is a disk cache option to add a body transformer that
// truncates responses based on match criteria.
func WithTruncator(priority TransformPriority, match func(string, int, string) bool) Option {
	t := Truncator{
		Priority: priority,
		Match:    match,
	}
	return option{
		cache: func(c *Cache) error {
			c.matcher.policy.BodyTransformers = append(c.matcher.policy.BodyTransformers, t)
			return nil
		},
		matcher: func(m *SimpleMatcher) error {
			m.policy.BodyTransformers = append(m.policy.BodyTransformers, t)
			return nil
		},
	}
}

// WithStatusCodeTrunactor is a disk cache option to add a body transformer
// that truncates responses when the status code is not in the provided list.
func WithStatusCodeTruncator(statusCodes ...int) Option {
	t := Truncator{
		Priority: TransformPriorityFirst,
		Match: func(_ string, statusCode int, _ string) bool {
			return !slices.Contains(statusCodes, statusCode)
		},
	}
	return option{
		cache: func(c *Cache) error {
			c.matcher.policy.BodyTransformers = append(c.matcher.policy.BodyTransformers, t)
			return nil
		},
		matcher: func(m *SimpleMatcher) error {
			m.policy.BodyTransformers = append(m.policy.BodyTransformers, t)
			return nil
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
		cache: func(c *Cache) error {
			c.matcher.policy.BodyTransformers = append(c.matcher.policy.BodyTransformers, t)
			return nil
		},
		matcher: func(m *SimpleMatcher) error {
			m.policy.BodyTransformers = append(m.policy.BodyTransformers, t)
			return nil
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
		cache: func(c *Cache) error {
			c.matcher.policy.BodyTransformers = append(c.matcher.policy.BodyTransformers, t)
			return nil
		},
		matcher: func(m *SimpleMatcher) error {
			m.policy.BodyTransformers = append(m.policy.BodyTransformers, t)
			return nil
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
		cache: func(c *Cache) error {
			c.matcher.policy.BodyTransformers = append(c.matcher.policy.BodyTransformers, t)
			return nil
		},
		matcher: func(m *SimpleMatcher) error {
			m.policy.BodyTransformers = append(m.policy.BodyTransformers, t)
			return nil
		},
	}
}

// WithMarshalUnmarshaler is a disk cache option to set a marshaler/unmarshaler.
func WithMarshalUnmarshaler(marshalUnmarshaler MarshalUnmarshaler) Option {
	return option{
		cache: func(c *Cache) error {
			c.matcher.policy.MarshalUnmarshaler = marshalUnmarshaler
			return nil
		},
		matcher: func(m *SimpleMatcher) error {
			m.policy.MarshalUnmarshaler = marshalUnmarshaler
			return nil
		},
	}
}

// WithGzipCompression is a disk cache option to set a gzip marshaler/unmarshaler.
func WithGzipCompression() Option {
	z := GzipMarshalUnmarshaler{
		Level: gzip.DefaultCompression,
	}
	return option{
		cache: func(c *Cache) error {
			c.matcher.policy.MarshalUnmarshaler = z
			return nil
		},
		matcher: func(m *SimpleMatcher) error {
			m.policy.MarshalUnmarshaler = z
			return nil
		},
	}
}

// WithZlibCompression is a disk cache option to set a zlib marshaler/unmarshaler.
func WithZlibCompression() Option {
	z := ZlibMarshalUnmarshaler{
		Level: zlib.DefaultCompression,
	}
	return option{
		cache: func(c *Cache) error {
			c.matcher.policy.MarshalUnmarshaler = z
			return nil
		},
		matcher: func(m *SimpleMatcher) error {
			m.policy.MarshalUnmarshaler = z
			return nil
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
		cache: func(c *Cache) error {
			c.matcher.policy.MarshalUnmarshaler = z
			return nil
		},
		matcher: func(m *SimpleMatcher) error {
			m.policy.MarshalUnmarshaler = z
			return nil
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
		cache: func(c *Cache) error {
			c.matcher.policy.MarshalUnmarshaler = z
			return nil
		},
		matcher: func(m *SimpleMatcher) error {
			m.policy.MarshalUnmarshaler = z
			return nil
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
		cache: func(c *Cache) error {
			c.matcher.policy.TTL = ttl
			return nil
		},
		matcher: func(m *SimpleMatcher) error {
			m.policy.TTL = ttl
			return nil
		},
	}
}

// WithIndexPath is a disk cache option to set the index path name.
func WithIndexPath(indexPath string) Option {
	return option{
		cache: func(c *Cache) error {
			c.matcher.indexPath = indexPath
			return nil
		},
		matcher: func(m *SimpleMatcher) error {
			m.indexPath = indexPath
			return nil
		},
	}
}

// WithLongPathHandler is a disk cache option to set a long path handler.
func WithLongPathHandler(longPathHandler func(string) string) Option {
	return option{
		cache: func(c *Cache) error {
			c.matcher.longPathHandler = longPathHandler
			return nil
		},
		matcher: func(m *SimpleMatcher) error {
			m.longPathHandler = longPathHandler
			return nil
		},
	}
}

// WithQueryEncoder is a disk cache option to set the query encoder.
func WithQueryEncoder(queryEncoder func(url.Values) string) Option {
	return option{
		cache: func(c *Cache) error {
			c.matcher.queryEncoder = queryEncoder
			return nil
		},
		matcher: func(m *SimpleMatcher) error {
			m.queryEncoder = queryEncoder
			return nil
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
				if !slices.Contains(fields, k) {
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
		cache: func(c *Cache) error {
			c.matcher.queryEncoder = f
			return nil
		},
		matcher: func(m *SimpleMatcher) error {
			m.queryEncoder = f
			return nil
		},
	}
}

// WithValidator is a disk cache option to set the cache policy validator.
func WithValidator(validator Validator) Option {
	return option{
		cache: func(c *Cache) error {
			c.matcher.policy.Validator = validator
			return nil
		},
		matcher: func(m *SimpleMatcher) error {
			m.policy.Validator = validator
			return nil
		},
	}
}

// WithValidatorFunc is a disk cache option to set the cache policy validator.
func WithValidatorFunc(f ValidatorFunc) Option {
	validator := NewSimpleValidator(f)
	return option{
		cache: func(c *Cache) error {
			c.matcher.policy.Validator = validator
			return nil
		},
		matcher: func(m *SimpleMatcher) error {
			m.policy.Validator = validator
			return nil
		},
	}
}

// WithContentTypeTTL is a disk cache option to set the cache policy TTL for
// matching content types.
func WithContentTypeTTL(ttl time.Duration, contentTypes ...string) Option {
	return WithValidatorFunc(func(_ *http.Request, res *http.Response, mod time.Time, _ bool, _ int) (Validity, error) {
		if ttl != 0 && time.Now().After(mod.Add(ttl)) && slices.Contains(contentTypes, res.Header.Get("Content-Type")) {
			return Retry, nil
		}
		return Valid, nil
	})
}

// WithRetryStatusCode is a disk cache option to add a validator to the cache
// policy that retries when the response status is not the expected status.
func WithRetryStatusCode(retries int, expected ...int) Option {
	return WithValidatorFunc(func(_ *http.Request, res *http.Response, mod time.Time, _ bool, count int) (Validity, error) {
		if retries < count && !slices.Contains(expected, res.StatusCode) {
			return Retry, nil
		}
		return Valid, nil
	})
}
