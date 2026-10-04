package testutil

import (
	"encoding/binary"
	"slices"
	"time"
)

// Box encodes an MP4 box with a 32-bit size.
func Box(boxType string, payload ...[]byte) []byte {
	body := slices.Concat(payload...)
	size := uint32(8 + len(body)) //nolint:gosec // Test fixtures are far below 4 GiB.
	return slices.Concat(binary.BigEndian.AppendUint32(nil, size), []byte(boxType), body)
}

// MP4 returns a minimal faststart MP4 whose version 0 movie header records duration.
func MP4(duration time.Duration) []byte {
	mvhd := make([]byte, 100)
	// The timescale counts milliseconds.
	binary.BigEndian.PutUint32(mvhd[12:], 1000)
	binary.BigEndian.PutUint32(mvhd[16:], uint32(duration.Milliseconds())) //nolint:gosec // Test durations fit in 49 days of milliseconds.
	return slices.Concat(
		Box("ftyp", []byte("isom\x00\x00\x02\x00isom")),
		Box("moov", Box("mvhd", mvhd)),
		Box("mdat", []byte("synthetic frames")),
	)
}
