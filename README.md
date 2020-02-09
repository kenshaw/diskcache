# diskcache [![GoDoc][godoc]][godoc-link]

Package `diskcache` provides a [`http.RoundTripper`][go-http-roundtripper] (for
use as a `http.Client.Transport`), that caches data on disk, or on any
[`afero.Fs`][afero] compatible implementation. Provides simple minification for
web content (HTML, CSS, JavaScript, etc), and compression/decompression to
reduce storage size on disk.

[godoc]: https://godoc.org/github.com/kenshaw/diskcache?status.svg (GoDoc)
[godoc-link]: https://godoc.org/github.com/kenshaw/diskcache

# Example

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
		diskcache.WithMatchers(
			diskcache.Match(
				`GET`,
				`^(?P<proto>https?)://github\.com$`,
				`^/?(?P<path>.*)$`,
				`{{proto}}/github/{{path}}`,
				diskcache.WithTTL(365*24*time.Hour),
			),
		),
		diskcache.WithStripHeaders(
			"Set-Cookie",
			"Content-Security-Policy",
			"Strict-Transport-Security",
			"Cache-[^:]*",
			"Vary",
			"Expect-[^:]*",
			"X-[^:]*",
		),
		diskcache.WithBasicMinifier(true),
		diskcache.WithGzipCompression(),
	)
	if err != nil {
		log.Fatal(err)
	}

	// build client
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

## "Why not X?" and other thoughts

There are existing `http.Client.Transport` implementations, such as
[`httpcache`][httpcache] that have been around for sometime. While `httpcache`
(and others) work perfectly well, `httpcache` mimics an actual HTTP Proxy and
thus allows the remote server to influence caching behavior (which `diskcache`
expressly ignores), does not work with `afero` package, and does not provide
simple/reusable ways to rewrite paths on disk.

`diskcache` was born out of general experimentation and necessity to control
retention and path rewriting policies on a per-method, per-host, and per-path
basis. To that end, I have made `diskcache` public in case it is helpful to
other people/projects.

It is my full intention to support `diskcache` and continue to add features,
resiliency, and robustness comparable to `httpcache` (just with more
configuration options out of the box!). That said, by no means should
`diskcache` be considered ready for general use in production by others at this
time. If you need a tried, tested, and true HTTP cache, please use
[`httpcache`][httpcache] until such time when this package is truly ready.

[httpcache]: https://github.com/gregjones/httpcache
[afero]: https://github.com/spf13/afero
[go-http-roundtripper]: https://golang.org/pkg/net/http/#RoundTripper
