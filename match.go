package diskcache

import (
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"

	"github.com/gobwas/glob"
)

// Matcher is the shared interface for rewriting URLs to disk paths.
type Matcher interface {
	// Match matches the passed request, returning the key and ttl.
	Match(*http.Request) (string, time.Duration, error)
}

// SimpleMatch is a simple path matcher.
type SimpleMatch struct {
	method      glob.Glob
	host        *regexp.Regexp
	hostSubexps []string
	path        *regexp.Regexp
	pathSubexps []string
	key         string
	ttl         time.Duration
	queryEscape bool
	queryPrefix string
}

// Match creates a new simple match for the provided method, host, path, and
// substitution key string.
func Match(method, host, path, key string, opts ...MatchOption) *SimpleMatch {
	hostRE := regexp.MustCompile(host)
	pathRE := regexp.MustCompile(path)
	m := &SimpleMatch{
		method:      glob.MustCompile(method, ','),
		host:        hostRE,
		hostSubexps: hostRE.SubexpNames(),
		path:        pathRE,
		pathSubexps: pathRE.SubexpNames(),
		key:         key,
	}
	for _, o := range opts {
		o(m)
	}
	return m
}

var fixRE = regexp.MustCompile(`/+`)

// Match satisifies the Matcher interface.
func (m *SimpleMatch) Match(req *http.Request) (string, time.Duration, error) {
	if !m.method.Match(req.Method) {
		return "", 0, nil
	}
	h := m.host.FindStringSubmatch(req.URL.Scheme + "://" + req.URL.Host)
	if h == nil {
		return "", 0, nil
	}
	p := m.path.FindStringSubmatch(req.URL.Path)
	if p == nil {
		return "", 0, nil
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
	if m.queryEscape {
		q := url.QueryEscape(req.URL.Query().Encode())
		if q != "" {
			q = m.queryPrefix + q
		}
		pairs = append(pairs, "{{query}}", q)
	}
	key := strings.NewReplacer(pairs...).Replace(m.key)
	return strings.TrimSuffix(fixRE.ReplaceAllString(key, "/"), "/"), m.ttl, nil
}

// MatchOption is a simple match option.
type MatchOption func(*SimpleMatch)

// WithTTL is a simple match option to set the TTL policy for matches.
func WithTTL(ttl time.Duration) MatchOption {
	return func(m *SimpleMatch) {
		m.ttl = ttl
	}
}

// WithQueryEscape is a simple match option to toggle escaping the query on rewritten URLs.
func WithQueryEscape(queryPrefix string) MatchOption {
	return func(m *SimpleMatch) {
		m.queryEscape, m.queryPrefix = true, queryPrefix
	}
}
