package filestream

import (
	"bufio"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"github.com/klauspost/compress/gzip"
	"github.com/stretchr/testify/assert"

	"github.com/stretchr/testify/require"
)

type offsetReader struct {
	offset  int
	scanner *bufio.Scanner
	mx      sync.Mutex
}

func (o *offsetReader) Offset() int {
	o.mx.Lock()
	defer o.mx.Unlock()
	return o.offset
}

// Seek sets the offset for the next Read or Write on file to offset, interpreted
// according to whence: 0 means relative to the origin of the file, 1 means
// relative to the current offset, and 2 means relative to the end.
// It returns the new offset and an error, if any.
// The behavior of Seek on a file opened with O_APPEND is not specified.

// Seek will read the file until offset,
func (o *offsetReader) Seek(offset int, whence int) (ret int, err error) {
	o.mx.Lock()
	defer o.mx.Unlock()

	switch whence {
	case 0:
		o.offset = offset
	case 1:
		o.offset += offset
	case 2:
		panic("unimplemented")
	default:
		return 0, fmt.Errorf("%d is an invalid whence", whence)
	}

	for i := range o.offset {
		if !o.scanner.Scan() {
			return i, fmt.Errorf("%d is an invalid offset", whence)
		}
	}

	return offset, nil
}

func (o *offsetReader) Scan() bool {
	return o.scanner.Scan()
}

func (o *offsetReader) Text() string {
	o.mx.Lock()
	defer o.mx.Unlock()
	o.offset++
	return o.scanner.Text()
}

func TestOffset(t *testing.T) {
	path := filepath.Join("testdata", "gzip", "10-logs.log.gz")

	gzFile, err := os.Open(path)
	require.NoErrorf(t, err, "could not open log file %s", path)
	defer gzFile.Close()

	gzr, err := gzip.NewReader(gzFile)
	require.NoErrorf(t, err, "could not create gzip reader")

	offset := 0
	offreader := &offsetReader{scanner: bufio.NewScanner(gzr)}

	fmt.Printf("reading 1st 5 lines\n\n")

	for range 5 {
		offreader.Scan()
		line := offreader.Text()
		fmt.Printf("\t%s\n", line)
		offset++
	}
	fmt.Printf("offset: %d\n", offset)
	fmt.Printf("offreader.Offset(): %d\n", offreader.Offset())
	fmt.Printf("\nread 1st 5 lines\n")

	gzFile2, err := os.Open(path)
	require.NoErrorf(t, err, "could not open log file %s", path)
	defer gzFile2.Close()

	gzr2, err := gzip.NewReader(gzFile2)
	require.NoErrorf(t, err, "could not create gzip reader")

	offset2 := 0
	offreader2 := &offsetReader{scanner: bufio.NewScanner(gzr2)}
	ret, err := offreader2.Seek(offset, 0)
	assert.Equalf(t, offset, ret, "unexpected seek advance. Got %d, want %d",
		ret, offset)
	for range 5 {
		offreader2.Scan()
		line := offreader2.Text()
		fmt.Printf("\t%s\n", line)
		offset2++
	}
	fmt.Printf("offset2: %d\n", offset2)
	fmt.Printf("offreader2.Offset(): %d\n", offreader2.Offset())
}
