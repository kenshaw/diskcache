package diskcache

import (
	"bytes"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"net/url"
	"regexp"
	"strings"

	"github.com/tdewolff/minify/v2"
	"github.com/tdewolff/minify/v2/css"
	"github.com/tdewolff/minify/v2/html"
	"github.com/tdewolff/minify/v2/js"
	"github.com/tdewolff/minify/v2/json"
	"github.com/tdewolff/minify/v2/svg"
	"github.com/tdewolff/minify/v2/xml"
)

// TransformPriority is the body transform priority.
type TransformPriority int

// Body mangle priorities.
const (
	TransformPriorityFirst  TransformPriority = 10
	TransformPriorityDecode TransformPriority = 50
	TransformPriorityModify TransformPriority = 60
	TransformPriorityMinify TransformPriority = 80
	TransformPriorityLast   TransformPriority = 90
)

// HeaderTransformer is the shared interface for modifying/altering headers
// prior to storage on disk.
type HeaderTransformer interface {
	HeaderTransform([]byte) []byte
}

// HeaderTransformerFunc is a header rewriter func.
type HeaderTransformerFunc func([]byte) []byte

// HeaderTransformer satisfies the HeaderTransformer interface.
func (f HeaderTransformerFunc) HeaderTransform(buf []byte) []byte {
	return f(buf)
}

// RegexpHeaderTransformer mangles headers matching regexps and replacements.
type RegexpHeaderTransformer struct {
	Regexps []*regexp.Regexp
	Repls   [][]byte
}

// NewHeaderTransformer creates a new header transformer from the passed
// matching regexp and replacement pairs.
func NewHeaderTransformer(pairs ...string) (*RegexpHeaderTransformer, error) {
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
	return &RegexpHeaderTransformer{Regexps: regexps, Repls: repls}, nil
}

// HeaderTransform satisfies the HeaderTransformer interface.
func (t *RegexpHeaderTransformer) HeaderTransform(buf []byte) []byte {
	lines := bytes.Split(buf, crlf)
	for i := 1; i < len(lines)-2; i++ {
		for j, re := range t.Regexps {
			line := append(crlf, append(lines[i], crlf...)...)
			if re.Match(line) {
				lines[i] = bytes.TrimSuffix(re.ReplaceAll(line, t.Repls[j]), crlf)
			}
		}
	}
	return bytes.Join(lines, crlf)
}

// BodyTransformer is the shared interface for mangling body content prior to
// storage in the fs.
type BodyTransformer interface {
	// TransformPriority returns the order for the transformer.
	TransformPriority() TransformPriority

	// BodyTransform mangles data from r to w for the provided URL, status
	// code, and content type. A return of false prevents further passing the
	// stream to lower priority body transformers.
	BodyTransform(w io.Writer, r io.Reader, urlstr string, code int, contentType string) (bool, error)
}

// Minifier is a body transformer that performs that minifies HTML, XML, SVG,
// JavaScript, JSON, and CSS content.
//
// See: github.com/tdewolff/minify
type Minifier struct {
	Priority TransformPriority
}

// TransformPriority satisfies the Transformer interface.
func (t Minifier) TransformPriority() TransformPriority {
	return t.Priority
}

// BodyTransform satisfies the BodyTransformer interface.
func (t Minifier) BodyTransform(w io.Writer, r io.Reader, urlstr string, code int, contentType string) (bool, error) {
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

// Truncator is a body transformer that truncates responses based on match
// criteria.
type Truncator struct {
	Priority TransformPriority
	Match    func(string, int, string) bool
}

// TransformPriority satisfies the BodyTransformer interface.
func (t Truncator) TransformPriority() TransformPriority {
	return t.Priority
}

// BodyTransform satisfies the BodyTransformer interface.
func (t Truncator) BodyTransform(w io.Writer, r io.Reader, urlstr string, code int, contentType string) (bool, error) {
	if t.Match(urlstr, code, contentType) {
		return false, nil
	}
	_, err := io.Copy(w, r)
	return err == nil, err
}

// Base64Decoder is a body transformer that base64 decodes the body.
type Base64Decoder struct {
	Priority     TransformPriority
	ContentTypes []string
	Encoding     *base64.Encoding
}

// TransformPriority satisfies the BodyTransformer interface.
func (t Base64Decoder) TransformPriority() TransformPriority {
	return t.Priority
}

// BodyTransform satisfies the BodyTransformer interface.
func (t Base64Decoder) BodyTransform(w io.Writer, r io.Reader, urlstr string, code int, contentType string) (bool, error) {
	if i := strings.Index(contentType, ";"); i != -1 {
		contentType = contentType[:i]
	}
	if len(t.ContentTypes) != 0 && !contains(t.ContentTypes, contentType) {
		_, err := io.Copy(w, r)
		return err == nil, err
	}
	encoding := t.Encoding
	if encoding == nil {
		encoding = base64.StdEncoding
	}
	dec := base64.NewDecoder(encoding, r)
	_, err := io.Copy(w, dec)
	return err == nil, err
}

// PrefixStripper is a body transformer that strips a prefix.
//
// Useful for munging content that may have had a preventative XSS prefix
// attached to it, such as certan JavaScript or JSON content.
type PrefixStripper struct {
	Priority     TransformPriority
	ContentTypes []string
	Prefix       []byte
}

// TransformPriority satisfies the BodyTransformer interface.
func (t PrefixStripper) TransformPriority() TransformPriority {
	return t.Priority
}

// BodyTransform satisfies the BodyTransformer interface.
func (t PrefixStripper) BodyTransform(w io.Writer, r io.Reader, urlstr string, code int, contentType string) (bool, error) {
	if i := strings.Index(contentType, ";"); i != -1 {
		contentType = contentType[:i]
	}
	if len(t.ContentTypes) != 0 && !contains(t.ContentTypes, contentType) {
		_, err := io.Copy(w, r)
		return err == nil, err
	}
	b := new(bytes.Buffer)
	if _, err := io.Copy(b, r); err != nil {
		return false, err
	}
	buf := b.Bytes()
	if !bytes.HasPrefix(buf, t.Prefix) {
		return false, fmt.Errorf("missing prefix %q", string(t.Prefix))
	}
	_, err := w.Write(buf[len(t.Prefix):])
	return err == nil, err
}
