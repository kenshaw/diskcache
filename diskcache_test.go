package diskcache

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/http/httputil"
	"os"
	"path/filepath"
	"strconv"
	"sync/atomic"
	"testing"
	"time"
)

func TestWithContextTTL(t *testing.T) {
	// set up simple test server for demonstration
	var count uint64
	s := httptest.NewServer(http.HandlerFunc(func(res http.ResponseWriter, req *http.Request) {
		fmt.Fprintf(res, "%d\n", atomic.AddUint64(&count, 1))
	}))
	defer s.Close()
	baseDir := setupDir(t, "test-with-context-ttl")
	// create disk cache
	c, err := New(
		WithBasePathFs(baseDir),
		WithErrorTruncator(),
		WithTTL(365*24*time.Hour),
	)
	if err != nil {
		t.Fatalf("expected no error, got: %v", err)
	}
	cl := &http.Client{
		Transport: c,
	}
	ctx := context.Background()
	for i := 0; i < 3; i++ {
		v, err := doReq(ctx, cl, s.URL)
		switch {
		case err != nil:
			t.Fatalf("expected no error, got: %v", err)
		case v != 1:
			t.Errorf("expected %d, got: %d", 1, v)
		}
	}
	if count != 1 {
		t.Fatalf("expected count == %d, got: %d", 1, count)
	}
	for i := 1; i < 5; i++ {
		v, err := doReq(WithContextTTL(ctx, 1*time.Millisecond), cl, s.URL)
		switch {
		case err != nil:
			t.Fatalf("expected no error, got: %v", err)
		case v != i+1:
			t.Errorf("expected %d, got: %d", i+1, v)
		}
		<-time.After(2 * time.Millisecond)
	}
}

func TestWithMethod(t *testing.T) {
	// set up simple test server for demonstration
	var count uint64
	const size = 20000
	s := httptest.NewServer(http.HandlerFunc(func(res http.ResponseWriter, req *http.Request) {
		if req.Method == "HEAD" {
			res.Header().Add("Content-Length", strconv.Itoa(size))
		} else {
			fmt.Fprintf(res, "%d\n", atomic.AddUint64(&count, 1))
		}
	}))
	defer s.Close()
	baseDir := setupDir(t, "test-with-method")
	// create disk cache
	c, err := New(
		WithBasePathFs(baseDir),
		WithMethod("GET", "HEAD"),
		WithErrorTruncator(),
		WithTTL(1*time.Hour),
	)
	if err != nil {
		t.Fatalf("expected no error, got: %v", err)
	}
	cl := &http.Client{
		Transport: c,
	}
	ctx := context.Background()
	for i := range 5 {
		req, err := http.NewRequestWithContext(ctx, "HEAD", s.URL, nil)
		if err != nil {
			t.Fatalf("expected no error, got: %v", err)
		}
		res, err := cl.Do(req)
		if err != nil {
			t.Fatalf("expected no error, got: %v", err)
		}
		buf, err := httputil.DumpResponse(res, true)
		if err != nil {
			t.Fatalf("expected no error, got: %v", err)
		}
		t.Logf("%d:\n%s\n---", i, string(buf))
		if res.ContentLength != size {
			t.Errorf("response %d expected size %d, got: %d", i, size, res.ContentLength)
		}
	}
	if count != 0 {
		t.Errorf("expected count %d, got: %d", 0, count)
	}
}

func doReq(ctx context.Context, cl *http.Client, urlstr string) (int, error) {
	req, err := http.NewRequestWithContext(ctx, "GET", urlstr, nil)
	if err != nil {
		return -1, err
	}
	res, err := cl.Do(req)
	if err != nil {
		return -1, err
	}
	defer res.Body.Close()
	buf, err := io.ReadAll(res.Body)
	if err != nil {
		return -1, err
	}
	return strconv.Atoi(string(bytes.TrimSpace(buf)))
}

func setupDir(t *testing.T, name string) string {
	t.Helper()
	wd, err := os.Getwd()
	if err != nil {
		t.Fatalf("expected no error, got: %v", err)
	}
	dir := filepath.Join(wd, ".cache", name)
	switch err := os.RemoveAll(dir); {
	case errors.Is(err, os.ErrNotExist):
	case err != nil:
		t.Fatalf("expected no error, got: %v", err)
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("expected no error, got: %v", err)
	}
	return dir
}
