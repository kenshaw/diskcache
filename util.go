package diskcache

import (
	"bytes"
	"io"
	"regexp"
)

// various byte slices.
var (
	crlf       = []byte("\r\n")
	crlfcrlf   = []byte("\r\n\r\n")
	httpHeader = []byte("HTTP/1.1 200 OK\r\n\r\n")
)

// predefined header strip funcs.
var (
	stripTransferEncodingHeader func([]byte) []byte
	stripContentLengthHeader    func([]byte) []byte
)

func init() {
	var err error
	stripTransferEncodingHeader, err = stripHeaders("Transfer-Encoding")
	if err != nil {
		panic(err)
	}
	stripContentLengthHeader, err = stripHeaders("Content-Length")
	if err != nil {
		panic(err)
	}
}

// stripHeaders builds a func that removes matching headers.
func stripHeaders(headers ...string) (HeaderTransformerFunc, error) {
	regexps, err := compileHeaderRegexps(`:.+?\r\n`, headers...)
	if err != nil {
		return nil, err
	}
	return func(buf []byte) []byte {
		for _, re := range regexps {
			for re.Match(buf) {
				buf = re.ReplaceAll(buf, crlf)
			}
		}
		return buf
	}, nil
}

// keepHeaders builds a func that removes all non-matching headers.
func keepHeaders(headers ...string) (HeaderTransformerFunc, error) {
	regexps, err := compileHeaderRegexps(`:.+?\r\n`, headers...)
	if err != nil {
		return nil, err
	}
	return func(buf []byte) []byte {
		lines := bytes.Split(bytes.TrimSpace(buf), crlf)
		keep := [][]byte{lines[0]}
		for i := 1; i < len(lines); i++ {
			for _, re := range regexps {
				if re.Match(append(crlf, append(lines[i], crlf...)...)) {
					keep = append(keep, lines[i])
					break
				}
			}
		}
		return bytes.Join(append(keep, nil, nil), crlf)
	}, nil
}

// compileHeaderRegexps compiles header regexps.
func compileHeaderRegexps(suffix string, headers ...string) ([]*regexp.Regexp, error) {
	regexps := make([]*regexp.Regexp, len(headers))
	for i := 0; i < len(headers); i++ {
		var err error
		if regexps[i], err = regexp.Compile(`(?i)\r\n` + headers[i] + suffix); err != nil {
			return nil, err
		}
	}
	return regexps, nil
}

// transformAndAppend walks the body transformer chain, applying each
// successive body transformer.
func transformAndAppend(buf []byte, r io.Reader, urlstr string, code int, contentType string, stripContentLength bool, bodyTransformers ...BodyTransformer) ([]byte, error) {
	for _, m := range bodyTransformers {
		w := new(bytes.Buffer)
		success, err := m.BodyTransform(w, r, urlstr, code, contentType)
		if err != nil {
			return nil, err
		}
		r = bytes.NewReader(w.Bytes())
		if !success {
			break
		}
	}
	body := new(bytes.Buffer)
	if _, err := io.Copy(body, r); err != nil {
		return nil, err
	}
	if stripContentLength {
		return append(stripContentLengthHeader(buf), body.Bytes()...), nil
	}
	return append(buf, body.Bytes()...), nil
}

// contains determines if haystack contains needle.
func contains(haystack []string, needle string) bool {
	for _, s := range haystack {
		if s == needle {
			return true
		}
	}
	return false
}

// containsInt determines if haystack contains needle.
func containsInt(haystack []int, needle int) bool {
	for _, s := range haystack {
		if s == needle {
			return true
		}
	}
	return false
}
