package diskcache

import (
	"compress/gzip"
	"compress/zlib"
	"io"
)

// CompressDecompressor is the shared interface for compression implementations
// for storage on disk.
type CompressDecompressor interface {
	Compress(w io.Writer, r io.Reader) error
	Decompress(w io.Writer, r io.Reader) error
}

// GzipCompressDecompressor is a gzip compressor/decompressor.
type GzipCompressDecompressor struct {
	// Level is the compression level.
	Level int
}

// Compress satisfies the CompressDecompressor interface.
func (z GzipCompressDecompressor) Compress(w io.Writer, r io.Reader) error {
	c, err := gzip.NewWriterLevel(w, z.Level)
	if err != nil {
		return err
	}
	_, err = io.Copy(c, r)
	if err != nil {
		return err
	}
	if err = c.Flush(); err != nil {
		return err
	}
	return c.Close()
}

// Decompress satisfies the CompressDecompressor interface.
func (z GzipCompressDecompressor) Decompress(w io.Writer, r io.Reader) error {
	d, err := gzip.NewReader(r)
	if err != nil {
		return err
	}
	_, err = io.Copy(w, d)
	return err
}

// ZlibCompressDecompressor is a zlib compressor/decompressor.
type ZlibCompressDecompressor struct {
	// Level is the compression level.
	Level int

	// Dict is the compression dictionary.
	Dict []byte
}

// Compress satisfies the CompressDecompressor interface.
func (z ZlibCompressDecompressor) Compress(w io.Writer, r io.Reader) error {
	c, err := zlib.NewWriterLevelDict(w, z.Level, z.Dict)
	if err != nil {
		return err
	}
	_, err = io.Copy(c, r)
	return err
}

// Decompress satisfies the CompressDecompressor interface.
func (z ZlibCompressDecompressor) Decompress(w io.Writer, r io.Reader) error {
	d, err := zlib.NewReaderDict(r, z.Dict)
	if err != nil {
		return err
	}
	_, err = io.Copy(w, d)
	return err
}
