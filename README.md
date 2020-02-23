# diskcache [![GoDoc][godoc]][godoc-link]

Package `diskcache` provides a standard Go HTTP transport
([`http.RoundTripper`][go-http-roundtripper]) implementation able to cache,
minify, compress, and transform HTTP responses on disk. Allows definition of
caching policies on a per-method, per-host, or per-path basis.

Provides a number of content transformers that alter cached HTTP response
headers and bodies (prior to storage on disk) including rewriting or
white/black-listing headers, stripping XSS prefixes, Base64 encoding/decoding,
webpage content minification, Gzip/Zlib compression, and more.

Package `diskcache` does not aim to work as a on-disk HTTP proxy. See
[github.com/gregjones/httpcache][httpcache] for a HTTP transport implementation
that provides a RFC 7234 compliant cache.

[godoc]: https://godoc.org/github.com/kenshaw/diskcache?status.svg (GoDoc)
[godoc-link]: https://godoc.org/github.com/kenshaw/diskcache

## Example

A basic Go example:

```go
// _example/example.go
package main

import (
	"fmt"
	"log"
	"net/http"
	"net/http/httputil"
	"os"
	"time"

	// "github.com/spf13/afero"
	"github.com/kenshaw/diskcache"
)

func main() {
	d, err := diskcache.New(
		// diskcache.WithFs(afero.New*(...)),
		diskcache.WithTTL(365*24*time.Hour),
		diskcache.WithHeaderWhitelist("Date", "Set-Cookie", "Content-Type"),
		diskcache.WithHeaderTransform(
			`Date:\s+(.+?)`, `Date: Not "$1"`,
		),
		diskcache.WithMinifier(),
		diskcache.WithErrorTruncator(),
		diskcache.WithGzipCompression(),
	)
	if err != nil {
		log.Fatal(err)
	}

	// create client using diskcache as the transport
	cl := &http.Client{
		Transport: d,
	}

	for i, urlstr := range []string{
		"https://github.com/kenshaw/diskcache",      // a path that exists
		"https://github.com/kenshaw/does-not-exist", // a path that doesn't
		// repeat
		"https://github.com/kenshaw/diskcache",
		"https://github.com/kenshaw/does-not-exist",
	} {
		if err := grab(cl, "GET", urlstr, i); err != nil {
			log.Fatal(err)
		}
	}
}

func grab(cl *http.Client, method, urlstr string, id int) error {
	req, err := http.NewRequest(method, urlstr, nil)
	if err != nil {
		return err
	}

	// execute request
	res, err := cl.Do(req)
	if err != nil {
		return err
	}

	fmt.Fprintf(os.Stdout, "------------------- %s %s (%d) -------------------\n", method, urlstr, id)
	buf, err := httputil.DumpResponse(res, true)
	if err != nil {
		return err
	}
	if _, err = os.Stdout.Write(buf); err != nil {
		return err
	}
	fmt.Fprintf(os.Stdout, "\n------------------- END %s %s (%d) -------------------\n\n", method, urlstr, id)
	return nil
}
```

See the [GoDoc listing][godoc] for more examples.

## Notes

Prior to writing `diskcache`, a number of the available HTTP transports were
were investigated to see if they could met the specific needs that `diskcache`
was designed for. In fact, it was found that many (notably [`httpcache`][httpcache]),
it was possible to accomplish what `diskcache` was designed for. However, it
required additional dependencies on non-standard (or uncommon) packages,
layering disk storage, or overcoming additional technical hurdles or obstacles
such as non-idiomatic code/design.

In short, no other packages were deemed to work "out-of-the-box", and thus
`diskcache` was born. `diskcache` has been made public in case it proves useful
to others.

`diskcache` *SHOULD NOT* be considered ready for general use in production by
others at this time. If you need a tried, tested, and true caching transport
layer for Go, please use [`httpcache`][httpcache].

[go-http-roundtripper]: https://golang.org/pkg/net/http/#RoundTripper
[httpcache]: https://github.com/gregjones/httpcache
[afero]: https://github.com/spf13/afero
[minify]: https://github.com/tdewolff/minify
