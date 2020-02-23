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
			`Date:(\s+).+?`, `Date:${1}TODAY`,
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
