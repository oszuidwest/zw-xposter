package article

import (
	"encoding/binary"
	"fmt"
	"io"
	"math"
	"time"
)

// X's duration limits for a post video on an account without Premium.
const (
	minVideoDuration = 500 * time.Millisecond
	maxVideoDuration = 20 * time.Minute
)

// mp4Duration reads the movie duration from moov/mvhd. Zero means the file
// does not record it, as in a fragmented MP4. Structural problems wrap
// ErrUnsupportedVideo; other errors come from reading r.
func mp4Duration(r io.ReadSeeker) (time.Duration, error) {
	end, err := r.Seek(0, io.SeekEnd)
	if err != nil {
		return 0, err
	}
	if _, err := r.Seek(0, io.SeekStart); err != nil {
		return 0, err
	}
	moovEnd, err := findBox(r, "moov", end)
	if err != nil {
		return 0, err
	}
	mvhdEnd, err := findBox(r, "mvhd", moovEnd)
	if err != nil {
		return 0, err
	}
	start, err := r.Seek(0, io.SeekCurrent)
	if err != nil {
		return 0, err
	}
	// Version and flags, creation and modification times, timescale, duration.
	var mvhd [32]byte
	n := min(mvhdEnd-start, int64(len(mvhd)))
	if _, err := io.ReadFull(r, mvhd[:n]); err != nil {
		return 0, err
	}
	need := int64(20)
	if mvhd[0] == 1 {
		need = 32
	}
	if n < need {
		return 0, fmt.Errorf("%w: mvhd box is truncated", ErrUnsupportedVideo)
	}
	var timescale uint32
	var units uint64
	if mvhd[0] == 1 {
		timescale = binary.BigEndian.Uint32(mvhd[20:])
		units = binary.BigEndian.Uint64(mvhd[24:])
		if units == math.MaxUint64 {
			units = 0
		}
	} else {
		timescale = binary.BigEndian.Uint32(mvhd[12:])
		units = uint64(binary.BigEndian.Uint32(mvhd[16:]))
		if units == math.MaxUint32 {
			units = 0
		}
	}
	if timescale == 0 {
		return 0, fmt.Errorf("%w: mvhd has no timescale", ErrUnsupportedVideo)
	}
	seconds := float64(units) / float64(timescale)
	if seconds >= math.MaxInt64/float64(time.Second) {
		return math.MaxInt64, nil
	}
	return time.Duration(seconds * float64(time.Second)), nil
}

// findBox scans the boxes from the current offset up to end and leaves r at the
// payload of the first box of type want, returning where that payload ends.
func findBox(r io.ReadSeeker, want string, end int64) (int64, error) {
	for {
		start, err := r.Seek(0, io.SeekCurrent)
		if err != nil {
			return 0, err
		}
		if end-start < 8 {
			return 0, fmt.Errorf("%w: MP4 has no %s box", ErrUnsupportedVideo, want)
		}
		var header [16]byte
		if _, err := io.ReadFull(r, header[:8]); err != nil {
			return 0, err
		}
		size, headerSize := int64(binary.BigEndian.Uint32(header[:4])), int64(8)
		switch size {
		case 0: // The box runs to the end of its container.
			size = end - start
		case 1: // A 64-bit size follows the type.
			if end-start < 16 {
				return 0, fmt.Errorf("%w: %q box is truncated", ErrUnsupportedVideo, header[4:8])
			}
			if _, err := io.ReadFull(r, header[8:]); err != nil {
				return 0, err
			}
			large := binary.BigEndian.Uint64(header[8:])
			if large > math.MaxInt64 {
				return 0, fmt.Errorf("%w: %q box size %d does not fit", ErrUnsupportedVideo, header[4:8], large)
			}
			size, headerSize = int64(large), 16
		}
		if size < headerSize || size > end-start {
			return 0, fmt.Errorf("%w: %q box size %d does not fit", ErrUnsupportedVideo, header[4:8], size)
		}
		if string(header[4:8]) == want {
			return start + size, nil
		}
		if _, err := r.Seek(start+size, io.SeekStart); err != nil {
			return 0, err
		}
	}
}
