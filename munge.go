package diskcache

import (
	"bytes"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strings"

	"github.com/tdewolff/minify"
	"github.com/tdewolff/minify/css"
	"github.com/tdewolff/minify/html"
	"github.com/tdewolff/minify/js"
	"github.com/tdewolff/minify/json"
	"github.com/tdewolff/minify/svg"
	"github.com/tdewolff/minify/xml"
)

// HeaderMunger is the shared interface for modifying/altering headers prior
// to storage on disk.
type HeaderMunger interface {
	HeaderMunge([]byte) []byte
}

// HeaderMungeFunc is a header rewriter func.
type HeaderMungerFunc func([]byte) []byte

// HeaderMunge satisfies the HeaderMunger interface.
func (f HeaderMungerFunc) HeaderMunge(buf []byte) []byte {
	return f(buf)
}

// RegexpHeaderMunger mangles headers matching regexps and replacements.
type RegexpHeaderMunger struct {
	Regexps []*regexp.Regexp
	Repls   [][]byte
}

// NewHeaderMunger creates a new header munger from the passed matching
// regexp and replacement pairs.
func NewHeaderMunger(pairs ...string) (*RegexpHeaderMunger, error) {
	n := len(pairs)
	if n%2 != 0 {
		return nil, errors.New("must have matching regexp and replacement pairs")
	}
	headers, repls := make([]string, n/2), make([][]byte, n/2)
	for i := 0; i < n; i += 2 {
		headers[i/2], repls[i/2] = pairs[i], append([]byte(pairs[i+1]), crlf...)
	}
	regexps, err := compileHeaderRegexps(`\r\n`, headers...)
	if err != nil {
		return nil, err
	}
	return &RegexpHeaderMunger{Regexps: regexps, Repls: repls}, nil
}

// HeaderMunge satisfies the HeaderMunger interface.
func (m *RegexpHeaderMunger) HeaderMunge(buf []byte) []byte {
	lines := bytes.Split(buf, crlf)
	for i := 1; i < len(lines)-2; i++ {
		for j, re := range m.Regexps {
			line := append(crlf, append(lines[i], crlf...)...)
			if re.Match(line) {
				lines[i] = bytes.TrimSuffix(re.ReplaceAll(line, m.Repls[j]), crlf)
			}
		}
	}
	return bytes.Join(lines, crlf)
}

// MungePriority is the body munge priority.
type MungePriority int

// Body mangle priorities.
const (
	MungePriorityFirst  MungePriority = 10
	MungePriorityDecode MungePriority = 50
	MungePriorityModify MungePriority = 60
	MungePriorityMinify MungePriority = 80
	MungePriorityLast   MungePriority = 90
)

// BodyMunger is the shared interface for mangling body content prior to
// storage in the fs.
type BodyMunger interface {
	// MungePriority returns the order for the munger.
	MungePriority() MungePriority

	// BodyMunge mangles data from r to w for the provided URL, status code,
	// and content type. A return of false prevents further passing the stream
	// to lower priority body mungers.
	BodyMunge(w io.Writer, r io.Reader, urlstr string, code int, contentType string) (bool, error)
}

// Minifier is a body munger that performs content minification, in
// order to reduce storage size on disk.
//
// Minifies HTML, XML, SVG, JavaScript, JSON, and CSS content.
//
// See: github.com/tdewolff/minify
type Minifier struct {
	Priority MungePriority
}

// MungePriority satisfies the Munger interface.
func (m Minifier) MungePriority() MungePriority {
	return m.Priority
}

// BodyMunge satisfies the BodyMunger interface.
func (m Minifier) BodyMunge(w io.Writer, r io.Reader, urlstr string, code int, contentType string) (bool, error) {
	if i := strings.Index(contentType, ";"); i != -1 {
		contentType = contentType[:i]
	}

	switch {
	default:
		_, err := io.Copy(w, r)
		return err == nil, err

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
			return false, err
		}
	}
	if err := z.Minify(contentType, w, r); err != nil {
		return false, err
	}
	return true, nil
}

var (
	jsContentTypeRE   = regexp.MustCompile("^(application|text)/(x-)?(java|ecma)script$")
	jsonContentTypeRE = regexp.MustCompile("[/+]json$")
	xmlContentTypeRE  = regexp.MustCompile("[/+]xml$")
)

// ErrorTruncator is a body munger that truncates the body entirely when a non
// HTTP status OK (200) response is returned.
type ErrorTruncator struct {
	Priority MungePriority
}

// MungePriority satisfies the BodyMunger interface.
func (m ErrorTruncator) MungePriority() MungePriority {
	return m.Priority
}

// BodyMunge satisfies the BodyMunger interface.
func (ErrorTruncator) BodyMunge(w io.Writer, r io.Reader, urlstr string, code int, contentType string) (bool, error) {
	if code != http.StatusOK {
		return false, nil
	}
	_, err := io.Copy(w, r)
	return err == nil, err
}

// Base64Decoder is a body munger that base64 decodes the body.
type Base64Decoder struct {
	Priority    MungePriority
	Encoding    *base64.Encoding
	ContentType string
}

// MungePriority satisfies the BodyMunger interface.
func (m Base64Decoder) MungePriority() MungePriority {
	return m.Priority
}

// BodyMunge satisfies the BodyMunger interface.
func (m Base64Decoder) BodyMunge(w io.Writer, r io.Reader, urlstr string, code int, contentType string) (bool, error) {
	if i := strings.Index(contentType, ";"); i != -1 {
		contentType = contentType[:i]
	}
	if m.ContentType != contentType {
		_, err := io.Copy(w, r)
		return err == nil, err
	}
	encoding := m.Encoding
	if encoding == nil {
		encoding = base64.StdEncoding
	}
	dec := base64.NewDecoder(encoding, r)
	_, err := io.Copy(w, dec)
	return err == nil, err
}

// PrefixStripper is a body munger that strips a prefix.
//
// Useful for munging content that may have had a preventative XSS prefix
// attached to it, such as certan JavaScript or JSON content.
type PrefixStripper struct {
	Priority    MungePriority
	Prefix      []byte
	ContentType string
}

// MungePriority satisfies the BodyMunger interface.
func (m PrefixStripper) MungePriority() MungePriority {
	return m.Priority
}

// BodyMunge satisfies the BodyMunger interface.
func (m PrefixStripper) BodyMunge(w io.Writer, r io.Reader, urlstr string, code int, contentType string) (bool, error) {
	if i := strings.Index(contentType, ";"); i != -1 {
		contentType = contentType[:i]
	}
	if m.ContentType != contentType {
		_, err := io.Copy(w, r)
		return err == nil, err
	}
	b := new(bytes.Buffer)
	if _, err := io.Copy(b, r); err != nil {
		return false, err
	}
	buf := b.Bytes()
	if !bytes.HasPrefix(buf, m.Prefix) {
		return false, fmt.Errorf("missing prefix %q", string(m.Prefix))
	}
	_, err := w.Write(buf[len(m.Prefix):])
	return err == nil, err
}
