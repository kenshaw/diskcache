package diskcache_test

import (
	"bytes"
	"fmt"
	"log"
	"net/http"
	"net/http/httptest"
	"net/http/httputil"
	"time"

	"github.com/kenshaw/diskcache"
)

// ExampleNew demonstrates setting up a simple diskcache for use with a
// http.Client.
func ExampleNew() {
	// set up simple test server for demonstration
	s := httptest.NewServer(http.HandlerFunc(func(res http.ResponseWriter, req *http.Request) {
		res.Header().Set("content-type", "text/html")
		res.Header().Set("X-Header", "test")
		fmt.Fprintf(res, `<!doctype html>
		<html lang="en">
			<head>
			</head>
			<body attribute="value">
				<p> hello %s! </p>
				<div> something </div>
				<a href="http://example.com/full/path">a link!</a>
			</body>
		</html>
`, req.URL.Query().Get("name"))
	}))
	defer s.Close()
	// create disk cache
	d, err := diskcache.New(
		// diskcache.WithBasePathFs("/path/to/cacheDir"),
		diskcache.WithAppCacheDir("diskcache-test"),
		diskcache.WithHeaderBlacklist("Set-Cookie", "Date"),
		diskcache.WithMinifier(),
		diskcache.WithErrorTruncator(),
		diskcache.WithGzipCompression(),
		diskcache.WithTTL(365*24*time.Hour),
	)
	if err != nil {
		log.Fatal(err)
	}
	// build and execute request
	cl := &http.Client{Transport: d}
	req, err := http.NewRequest("GET", s.URL+"/hello?name=ken", nil)
	if err != nil {
		log.Fatal(err)
	}
	res, err := cl.Do(req)
	if err != nil {
		log.Fatal(err)
	}
	defer res.Body.Close()
	// dump
	buf, err := httputil.DumpResponse(res, true)
	if err != nil {
		log.Fatal(err)
	}
	fmt.Println(string(bytes.ReplaceAll(buf, []byte("\r\n"), []byte("\n"))))
	// Output:
	// HTTP/1.1 200 OK
	// Connection: close
	// Content-Type: text/html
	// X-Header: test
	//
	// <!doctype html><html lang=en><body attribute=value><p>hello ken!<div>something</div><a href=//example.com/full/path>a link!</a>
}
