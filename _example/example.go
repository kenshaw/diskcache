package main

import (
	"log"
	"net/http"
	"net/http/httputil"
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
	req, err := http.NewRequest("GET", "https://github.com/kenshaw/diskcache", nil)
	if err != nil {
		log.Fatal(err)
	}

	// execute request
	res1, err := cl.Do(req)
	if err != nil {
		log.Fatal(err)
	}
	buf1, err := httputil.DumpResponse(res1, true)
	if err != nil {
		log.Fatal(err)
	}
	log.Printf("> req1:\n%s\n\n", string(buf1))

	// repeat request
	res2, err := cl.Do(req)
	if err != nil {
		log.Fatal(err)
	}
	buf2, err := httputil.DumpResponse(res2, true)
	if err != nil {
		log.Fatal(err)
	}
	log.Printf("> req2:\n%s\n\n", string(buf2))
}
