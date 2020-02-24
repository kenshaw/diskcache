package diskcache

import (
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
	method       glob.Glob
	host         *regexp.Regexp
	hostSubexps  []string
	path         *regexp.Regexp
	pathSubexps  []string
	key          string
	indexPath    string
	queryEncoder func(url.Values) string
	policy       Policy
}

// Match creates a new simple match for the provided method, host, path, and
// substitution key string.
func Match(method, host, path, key string, opts ...Option) *SimpleMatcher {
	hostRE := regexp.MustCompile(host)
	pathRE := regexp.MustCompile(path)
	m := &SimpleMatcher{
		method:      glob.MustCompile(method, ','),
		host:        hostRE,
		hostSubexps: hostRE.SubexpNames(),
		path:        pathRE,
		pathSubexps: pathRE.SubexpNames(),
		key:         key,
	}
	for _, o := range opts {
		o.simpleMatcher(m)
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
	return strings.TrimSuffix(fixRE.ReplaceAllString(key, "/"), "/"), m.policy, nil
}

// cache satisfies the Option interface.
func (m *SimpleMatcher) cache(c *Cache) error {
	if !c.noDefault {
		if m.policy.TTL == 0 {
			m.policy.TTL = c.matcher.policy.TTL
		}
		m.policy.HeaderTransformers = append(c.matcher.policy.HeaderTransformers, m.policy.HeaderTransformers...)
		m.policy.BodyTransformers = append(c.matcher.policy.BodyTransformers, m.policy.BodyTransformers...)
		if m.policy.MarshalUnmarshaler == nil {
			m.policy.MarshalUnmarshaler = c.matcher.policy.MarshalUnmarshaler
		}
	}
	c.matchers = append(c.matchers, m)
	return nil
}

// simpleMatcher satisfies the Option interface.
func (m *SimpleMatcher) simpleMatcher(*SimpleMatcher) {
	panic("not supported")
}
