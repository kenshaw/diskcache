package diskcache

import (
	"bytes"
	"regexp"
)

// compileHeaderRegexps compiles header regexps.
func compileHeaderRegexps(suffix string, headers ...string) ([]*regexp.Regexp, error) {
	var err error
	regexps := make([]*regexp.Regexp, len(headers))
	for i := 0; i < len(headers); i++ {
		regexps[i], err = regexp.Compile(`(?i)\r\n` + headers[i] + suffix)
		if err != nil {
			return nil, err
		}
	}
	return regexps, nil
}

// crlf is the line ending.
var crlf = []byte{'\r', '\n'}

// keepHeaders builds a func that removes all non-matching headers.
func keepHeaders(headers ...string) (HeaderMungerFunc, error) {
	regexps, err := compileHeaderRegexps(`:.+?\r\n`, headers...)
	if err != nil {
		return nil, err
	}
	return func(buf []byte) []byte {
		lines := bytes.Split(buf, crlf)
		for i := len(lines) - 3; i > 0; i-- {
			var keep bool
			for _, re := range regexps {
				if re.Match(append(crlf, append(lines[i], crlf...)...)) {
					keep = true
					break
				}
			}
			if !keep {
				lines = append(lines[:i], lines[i+1:]...)
			}
		}
		return bytes.Join(lines, crlf)
	}, nil
}

// stripHeaders builds a func that removes matching headers.
func stripHeaders(headers ...string) (HeaderMungerFunc, error) {
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

// Predefined header strip funcs.
var stripTransferEncodingHeader func([]byte) []byte
var stripContentLengthHeader func([]byte) []byte

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
