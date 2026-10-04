package article

import (
	"bytes"
	"encoding/binary"
	"errors"
	"math"
	"slices"
	"testing"
	"time"

	"github.com/oszuidwest/zw-xposter/internal/testutil"
)

func mvhdV0(timescale, units uint32) []byte {
	payload := make([]byte, 100)
	binary.BigEndian.PutUint32(payload[12:], timescale)
	binary.BigEndian.PutUint32(payload[16:], units)
	return testutil.Box("mvhd", payload)
}

func mvhdV1(timescale uint32, units uint64) []byte {
	payload := make([]byte, 112)
	payload[0] = 1
	binary.BigEndian.PutUint32(payload[20:], timescale)
	binary.BigEndian.PutUint64(payload[24:], units)
	return testutil.Box("mvhd", payload)
}

// largeBox encodes a box with a 64-bit size field.
func largeBox(boxType string, size uint64, payload []byte) []byte {
	header := binary.BigEndian.AppendUint32(nil, 1)
	header = append(header, boxType...)
	return slices.Concat(binary.BigEndian.AppendUint64(header, size), payload)
}

func TestMP4Duration(t *testing.T) {
	t.Parallel()
	ftyp := testutil.Box("ftyp", []byte("isom"))
	mdat := testutil.Box("mdat", []byte("frames"))
	moov := func(mvhd []byte) []byte { return testutil.Box("moov", mvhd) }
	for _, tt := range []struct {
		name        string
		file        []byte
		want        time.Duration
		unsupported bool
	}{
		{name: "faststart", file: testutil.MP4(90 * time.Second), want: 90 * time.Second},
		{name: "moov after mdat", file: slices.Concat(ftyp, mdat, moov(mvhdV0(25, 250))), want: 10 * time.Second},
		{name: "version 1 header", file: slices.Concat(ftyp, moov(mvhdV1(90_000, 90_000*30))), want: 30 * time.Second},
		{name: "64-bit box size", file: slices.Concat(largeBox("mdat", 22, []byte("frames")), moov(mvhdV0(1000, 1500))), want: 1500 * time.Millisecond},
		{name: "last box runs to the end", file: slices.Concat(ftyp, []byte{0, 0, 0, 0}, []byte("moov"), mvhdV0(1000, 2000)), want: 2 * time.Second},
		{name: "huge duration saturates", file: moov(mvhdV1(1, math.MaxUint64-1)), want: math.MaxInt64},
		{name: "unrecorded duration", file: moov(mvhdV0(1000, 0))},
		{name: "unknown version 0 duration", file: moov(mvhdV0(1000, math.MaxUint32))},
		{name: "unknown version 1 duration", file: moov(mvhdV1(1000, math.MaxUint64))},
		{name: "empty file", unsupported: true},
		{name: "not an MP4", file: []byte("<html>not a video</html>"), unsupported: true},
		{name: "no moov", file: slices.Concat(ftyp, mdat), unsupported: true},
		{name: "no mvhd", file: slices.Concat(ftyp, testutil.Box("moov", testutil.Box("trak"))), unsupported: true},
		{name: "zero timescale", file: moov(mvhdV0(0, 1000)), unsupported: true},
		{name: "empty mvhd at the end of the file", file: moov(testutil.Box("mvhd")), unsupported: true},
		{name: "truncated mvhd", file: moov(testutil.Box("mvhd", make([]byte, 19))), unsupported: true},
		{name: "truncated version 1 mvhd", file: moov(testutil.Box("mvhd", append([]byte{1}, make([]byte, 30)...))), unsupported: true},
		{name: "box larger than file", file: slices.Concat(ftyp, binary.BigEndian.AppendUint32(nil, 1<<20), []byte("moov")), unsupported: true},
		{name: "box smaller than its header", file: slices.Concat(binary.BigEndian.AppendUint32(nil, 4), []byte("ftyp"), moov(mvhdV0(1000, 1000))), unsupported: true},
		{name: "64-bit size overflows", file: largeBox("mdat", math.MaxUint64, nil), unsupported: true},
		{name: "truncated 64-bit header", file: slices.Concat(binary.BigEndian.AppendUint32(nil, 1), []byte("mdat"), []byte{0, 0}), unsupported: true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got, err := mp4Duration(bytes.NewReader(tt.file))
			testutil.Equal(t, errors.Is(err, ErrUnsupportedVideo), tt.unsupported)
			if !tt.unsupported {
				testutil.NoError(t, err)
				testutil.Equal(t, got, tt.want)
			}
		})
	}
}
