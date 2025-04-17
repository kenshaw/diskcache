package diskcache

import (
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"strings"

	"github.com/gobwas/glob"
)

// Matcher is the shared interface for retrivieving a disk cache policy for
// requests.
type Matcher interface {
	// Match matches the passed request, returning the key and ttl.
	Match(*http.Request) (string, Policy, error)
}

// SimpleMatcher handles matching caching policies to requests.
type SimpleMatcher struct {
	method          glob.Glob
	host            *regexp.Regexp
	hostSubexps     []string
	path            *regexp.Regexp
	pathSubexps     []string
	key             string
	indexPath       string
	longPathHandler func(string) string
	queryEncoder    func(url.Values) string
	policy          Policy
}

// NewSimpleMatcher creates a simple matcher for the provided method, host and
// path regular expressions, substitution key string, and other options.
//
// Example:
//
//	m, err := NewSimpleMatcher(
//		`GET`,
//		`^(?P<proto>https?)://(?P<host>[^:]+)(?P<port>:[0-9]+)?$`,
//		`^/?(?P<path>.*)$`,
//		`{{proto}}/{{host}}{{port}}/{{path}}{{query}}`,
//		WithIndexPath("?index"),
//		WithQueryPrefix("_"),
//		WithLongPathHandler(func(key string) string {
//			if len(key) > 128 {
//				return fmt.Sprintf("?long/%x", sha256.Sum256([]byte(key)))
//			}
//			return key
//		}),
//	)
//	if err != nil {
//		return nil, err
//	}
func NewSimpleMatcher(method, host, path, key string, opts ...Option) (*SimpleMatcher, error) {
	methodGlob, err := glob.Compile(method, ',')
	if err != nil {
		return nil, err
	}
	hostRE, err := regexp.Compile(host)
	if err != nil {
		return nil, err
	}
	pathRE, err := regexp.Compile(path)
	if err != nil {
		return nil, err
	}
	m := &SimpleMatcher{
		method:      methodGlob,
		host:        hostRE,
		hostSubexps: hostRE.SubexpNames(),
		path:        pathRE,
		pathSubexps: pathRE.SubexpNames(),
		key:         key,
	}
	for _, o := range opts {
		if err := o.apply(m); err != nil {
			return nil, err
		}
	}
	return m, nil
}

// Match creates a simple matcher for the provided method, host and path
// regular expressions, and substitution key string. Wraps NewSimpleMatcher.
func Match(method, host, path, key string, opts ...Option) Matcher {
	m, err := NewSimpleMatcher(method, host, path, key, opts...)
	if err != nil {
		panic(err)
	}
	return m
}

var fixRE = regexp.MustCompile(`/+`)

// Match satisifies the Matcher interface.
func (m *SimpleMatcher) Match(req *http.Request) (string, Policy, error) {
	if !m.method.Match(req.Method) {
		return "", Policy{}, nil
	}
	h := m.host.FindStringSubmatch(req.URL.Scheme + "://" + req.URL.Host)
	if h == nil {
		return "", Policy{}, nil
	}
	p := m.path.FindStringSubmatch(req.URL.Path)
	if p == nil {
		return "", Policy{}, nil
	}
	pairs := []string{"{{method}}", strings.ToLower(req.Method)}
	for i := 1; i < len(m.hostSubexps); i++ {
		if m.hostSubexps[i] == "" {
			continue
		}
		pairs = append(pairs, "{{"+m.hostSubexps[i]+"}}", h[i])
	}
	for i := 1; i < len(m.pathSubexps); i++ {
		if m.pathSubexps[i] == "" {
			continue
		}
		pairs = append(pairs, "{{"+m.pathSubexps[i]+"}}", p[i])
	}
	if m.queryEncoder != nil {
		pairs = append(pairs, "{{query}}", m.queryEncoder(req.URL.Query()))
	}
	key := strings.NewReplacer(pairs...).Replace(m.key)
	if key == "" || strings.HasSuffix(key, "/") {
		key += m.indexPath
	}
	key = strings.TrimSuffix(fixRE.ReplaceAllString(key, "/"), "/")
	if m.longPathHandler != nil {
		key = m.longPathHandler(key)
	}
	return key, m.policy, nil
}

// apply satisfies the Option interface.
func (m *SimpleMatcher) apply(v any) error {
	switch z := v.(type) {
	case *Cache:
		if !z.noDefault {
			if m.policy.TTL == 0 {
				m.policy.TTL = z.matcher.policy.TTL
			}
			m.policy.HeaderTransformers = append(z.matcher.policy.HeaderTransformers, m.policy.HeaderTransformers...)
			m.policy.BodyTransformers = append(z.matcher.policy.BodyTransformers, m.policy.BodyTransformers...)
			if m.policy.MarshalUnmarshaler == nil {
				m.policy.MarshalUnmarshaler = z.matcher.policy.MarshalUnmarshaler
			}
		}
		z.matchers = append(z.matchers, m)
		return nil
	}
	return fmt.Errorf("SimpleMatcher does not apply to %T", v)
}
