# diskcache [![Go Package][gopkg]][gopkg-link]

Package `diskcache` provides a standard Go HTTP transport
([`http.RoundTripper`][go-http-roundtripper]) implementation designed to cache,
minify, compress, and transform HTTP responses on disk. Allows definition of
caching policies on a per-method, per-host, or per-path basis.

Additionally provides header, body and content transformers that alter cached
HTTP response headers and bodies prior to storage on disk. Includes ability to
rewrite headers, white/black-list headers, strip XSS prefixes, Base64
encode/decode content, minify content, and marshal/unmarshal data stored on
disk using Go's GLib/ZLib compression.

Package `diskcache` _does not_ act as an on-disk HTTP proxy. Please see
[github.com/gregjones/httpcache][httpcache] for a HTTP transport implementation
that provides a RFC 7234 compliant cache.

[gopkg]: https://pkg.go.dev/badge/github.com/kenshaw/diskcache.svg (Go Package)
[gopkg-link]: https://pkg.go.dev/github.com/kenshaw/diskcache

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

See the [Go package documentation][gopkg] for more examples.

## Afero support

The [`afero` filesystem package][afero] can be used in conjunction with
`diskcache` to satisfy advanced use-cases such as using an in-memory cache, or
storing on a remote filesystem.

## Notes

Prior to writing `diskcache`, a number of HTTP transport packages were
investigated to see if they could meet the specific needs that `diskcache` was
designed for. There are in fact a few other transport packages that provide
similar functionality as `diskcache` (notably [`httpcache`][httpcache]),
however after extensive evaluation, it was decided that existing package
implementations did not meet all requirements.

[go-http-roundtripper]: https://golang.org/pkg/net/http/#RoundTripper
[httpcache]: https://github.com/gregjones/httpcache
[afero]: https://github.com/spf13/afero
[minify]: https://github.com/tdewolff/minify
