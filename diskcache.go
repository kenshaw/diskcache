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
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"io/fs"
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
	m, err := NewSimpleMatcher(
		`GET`,
		`^(?P<proto>https?)://(?P<host>[^:]+)(?P<port>:[0-9]+)?$`,
		`^/?(?P<path>.*)$`,
		`{{proto}}/{{host}}{{port}}/{{path}}{{query}}`,
		WithIndexPath("?index"),
		WithQueryPrefix("_"),
		WithLongPathHandler(func(key string) string {
			if len(key) > 128 {
				return fmt.Sprintf("?long/%x", sha256.Sum256([]byte(key)))
			}
			return key
		}),
	)
	if err != nil {
		return nil, err
	}
	c := &Cache{
		dirMode:  0o755,
		fileMode: 0o644,
		matcher:  m,
	}
	for _, o := range opts {
		if err := o.apply(c); err != nil {
			return nil, err
		}
	}
	// set default fs as overlay at <working directory>/cache
	if c.fs == nil {
		dir, err := os.Getwd()
		if err != nil {
			return nil, err
		}
		if err := WithBasePathFs(filepath.Join(dir, "cache")).apply(c); err != nil {
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
	// match policy for the request
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
	force := false
	for {
		// fetch
		stale, mod, res, err := c.Fetch(key, p, req, force)
		switch {
		case err != nil:
			return nil, err
		case p.Validator == nil:
			return res, nil
		}
		// validate response
		validity, err := p.Validator.Validate(req, res, mod, stale)
		switch {
		case err != nil:
			return nil, err
		case validity == Error:
			return nil, fmt.Errorf("%T returned no error, but returned Error validity", p.Validator)
		case validity == Retry:
			force = true
		case validity == Valid:
			return res, nil
		default:
			return nil, fmt.Errorf("unable to handle %T validity %d", p.Validator, validity)
		}
	}
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

// EvictKey forces a cache eviction (deletion) of the specified key.
func (c *Cache) EvictKey(key string) error {
	return c.fs.Remove(key)
}

// Fetch retrieves the key from the cache based on the policy TTL. When forced,
// or if the cached response is stale the request will be executed and the
// response cached.
func (c *Cache) Fetch(key string, p Policy, req *http.Request, force bool) (bool, time.Time, *http.Response, error) {
	// check stale
	stale, mod, err := c.Stale(req.Context(), key, p.TTL)
	if err != nil {
		return false, time.Time{}, nil, err
	}
	// exec when stale or forced
	if stale || force {
		res, err := c.Exec(key, p, req)
		if err != nil {
			return false, time.Time{}, nil, err
		}
		mod, err := c.Mod(key)
		if err != nil {
			return false, time.Time{}, nil, err
		}
		return false, mod, res, nil
	}
	// load
	res, err := c.Load(key, p, req)
	if err != nil {
		return false, time.Time{}, nil, err
	}
	return true, mod, res, nil
}

// Mod returns last modified time of the key.
func (c *Cache) Mod(key string) (time.Time, error) {
	fi, err := c.fs.Stat(key)
	switch {
	case err != nil:
		return time.Time{}, err
	case fi.IsDir():
		return time.Time{}, fmt.Errorf("fs path %q is a directory", key)
	}
	return fi.ModTime(), nil
}

// Stale returns whether or not the key is stale, based on ttl.
func (c *Cache) Stale(ctx context.Context, key string, ttl time.Duration) (bool, time.Time, error) {
	mod, err := c.Mod(key)
	switch {
	case err != nil && errors.Is(err, fs.ErrNotExist):
		return true, mod, nil
	case err != nil:
		return false, time.Time{}, err
	}
	if d, ok := TTL(ctx); ok {
		ttl = d
	}
	return ttl != 0 && time.Now().After(mod.Add(ttl)), mod, nil
}

// Cached returns whether or not the request is cached. Wraps Match, Stale.
func (c *Cache) Cached(req *http.Request) (bool, error) {
	key, p, err := c.Match(req)
	if err != nil {
		return false, err
	}
	stale, _, err := c.Stale(req.Context(), key, p.TTL)
	if err != nil {
		return false, err
	}
	return !stale, nil
}

// Load unmarshals and loads the cached response for the key and cache policy.
func (c *Cache) Load(key string, p Policy, req *http.Request) (*http.Response, error) {
	f, err := c.fs.OpenFile(key, os.O_RDONLY, 0)
	if err != nil {
		return nil, err
	}
	var r io.Reader = f
	if p.MarshalUnmarshaler != nil {
		buf := new(bytes.Buffer)
		if err := p.MarshalUnmarshaler.Unmarshal(buf, f); err != nil {
			return nil, err
		}
		r = buf
	}
	return http.ReadResponse(bufio.NewReader(r), req)
}

// Exec executes the request, storing the response using the key and cache
// policy. Applies header and body transformers, before marshaling and the
// response.
func (c *Cache) Exec(key string, p Policy, req *http.Request) (*http.Response, error) {
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
	// dump
	buf, err := httputil.DumpResponse(res, false)
	if err != nil {
		return nil, err
	}
	// strip Transfer-Encoding and apply header transforms
	buf = stripTransferEncodingHeader(buf)
	for _, t := range p.HeaderTransformers {
		buf = t.HeaderTransform(buf)
	}
	// apply body transforms
	buf, err = transformAndAppend(
		buf,
		res.Body,
		req.URL.String(),
		res.StatusCode,
		res.Header.Get("Content-Type"),
		req.Method != "HEAD",
		p.BodyTransformers...,
	)
	if err != nil {
		return nil, err
	}
	body := buf
	// marshal
	if p.MarshalUnmarshaler != nil {
		b := new(bytes.Buffer)
		if err := p.MarshalUnmarshaler.Marshal(b, bytes.NewReader(buf)); err != nil {
			return nil, err
		}
		buf = b.Bytes()
	}
	// store
	if len(buf) != 0 {
		// ensure path exists
		if err := c.fs.MkdirAll(path.Dir(key), c.dirMode); err != nil {
			return nil, err
		}
		// open cache file
		f, err := c.fs.OpenFile(key, os.O_APPEND|os.O_CREATE|os.O_WRONLY|os.O_TRUNC, c.fileMode)
		if err != nil {
			return nil, err
		}
		if _, err := f.Write(buf); err != nil {
			return nil, err
		}
		if err := f.Close(); err != nil {
			return nil, err
		}
	}
	// read response
	return http.ReadResponse(bufio.NewReader(bytes.NewReader(body)), req)
}

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
	// Validator validates responses.
	Validator Validator
}

// UserCacheDir returns the user's system cache dir, adding paths to the end.
//
// Example usage:
//
//	dir, err := diskcache.UserCacheDir("my-app-name")
//	cache, err := diskcache.New(diskcache.WithBasePathFs(dir))
//
// Note: WithAppCacheDir is easier.
func UserCacheDir(paths ...string) (string, error) {
	dir, err := os.UserCacheDir()
	if err != nil {
		return "", err
	}
	if dir, err = realpath.Realpath(dir); err != nil {
		return "", err
	}
	return filepath.Join(append([]string{dir}, paths...)...), nil
}

// contextKey is a context key.
type contextKey string

// context keys.
const (
	ttlKey contextKey = "ttl"
)

// WithContextTTL adds the ttl to the context.
func WithContextTTL(parent context.Context, ttl time.Duration) context.Context {
	return context.WithValue(parent, ttlKey, ttl)
}

// TTL returns the ttl from the context.
func TTL(ctx context.Context) (time.Duration, bool) {
	ttl, ok := ctx.Value(ttlKey).(time.Duration)
	return ttl, ok
}
