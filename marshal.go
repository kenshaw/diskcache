package diskcache

import (
	"compress/gzip"
	"compress/zlib"
	"io"
)

// MarshalUnmarshaler is the shared interface for marshaling/unmarshaling.
type MarshalUnmarshaler interface {
	Marshal(w io.Writer, r io.Reader) error
	Unmarshal(w io.Writer, r io.Reader) error
}

// GzipMarshalUnmarshaler is a gzip mashaler/unmarshaler.
type GzipMarshalUnmarshaler struct {
	// Level is the compression level.
	Level int
}

// Marshal satisfies the MarshalUnmarshaler interface.
func (z GzipMarshalUnmarshaler) Marshal(w io.Writer, r io.Reader) error {
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

// Unmarshal satisfies the MarshalUnmarshaler interface.
func (z GzipMarshalUnmarshaler) Unmarshal(w io.Writer, r io.Reader) error {
	d, err := gzip.NewReader(r)
	if err != nil {
		return err
	}
	_, err = io.Copy(w, d)
	return err
}

// ZlibMarshalUnmarshaler is a zlib mashaler/unmarshaler.
type ZlibMarshalUnmarshaler struct {
	// Level is the compression level.
	Level int

	// Dict is the compression dictionary.
	Dict []byte
}

// Marshal satisfies the MarshalUnmarshaler interface.
func (z ZlibMarshalUnmarshaler) Marshal(w io.Writer, r io.Reader) error {
	c, err := zlib.NewWriterLevelDict(w, z.Level, z.Dict)
	if err != nil {
		return err
	}
	_, err = io.Copy(c, r)
	return err
}

// Unmarshal satisfies the MarshalUnmarshaler interface.
func (z ZlibMarshalUnmarshaler) Unmarshal(w io.Writer, r io.Reader) error {
	d, err := zlib.NewReaderDict(r, z.Dict)
	if err != nil {
		return err
	}
	_, err = io.Copy(w, d)
	return err
}

// FlatMarshalUnmarshaler is a flat file mashaler/unmarshaler, that
// removes / restores the HTTP header.
type FlatMarshalUnmarshaler struct{}
