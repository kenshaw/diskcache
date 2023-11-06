package diskcache

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
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
