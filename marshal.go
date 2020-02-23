package diskcache

import (
	"bytes"
	"compress/gzip"
	"compress/zlib"
	"errors"
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

// FlatMarhsalUnmarshaler is a flat file marshaler/unmarshaler, dropping
// original response header when marshaling.
type FlatMarshalUnmarshaler struct {
	// Chain is an additional MarshalUnmarshaler that the data can be sent to
	// prior to storage on disk, but after the header has been stripped.
	Chain MarshalUnmarshaler
}

// Marshal satisfies the MarshalUnmarshaler interface.
func (z FlatMarshalUnmarshaler) Marshal(w io.Writer, r io.Reader) error {
	b := new(bytes.Buffer)
	_, err := io.Copy(b, r)
	if err != nil {
		return err
	}
	buf := b.Bytes()
	i := bytes.Index(buf, append(crlf, crlf...))
	if i == -1 {
		return errors.New("unable to find header/body boundary")
	}
	if z.Chain == nil {
		_, err = w.Write(buf[i+4:])
		return err
	}
	return z.Chain.Marshal(w, bytes.NewReader(buf[i+4:]))
}

// Unmarshal satisfies the MarshalUnmarshaler interface.
func (z FlatMarshalUnmarshaler) Unmarshal(w io.Writer, r io.Reader) error {
	if z.Chain != nil {
		b := new(bytes.Buffer)
		if err := z.Chain.Unmarshal(b, r); err != nil {
			return err
		}
		r = b
	}
	_, err := w.Write(httpHeader)
	if err != nil {
		return err
	}
	_, err = io.Copy(w, r)
	return err
}
