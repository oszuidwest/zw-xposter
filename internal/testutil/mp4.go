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

// MVHD encodes a version 0 movie header box recording units of timescale.
func MVHD(timescale, units uint32) []byte {
	payload := make([]byte, 100)
	binary.BigEndian.PutUint32(payload[12:], timescale)
	binary.BigEndian.PutUint32(payload[16:], units)
	return Box("mvhd", payload)
}

// MP4 returns a minimal faststart MP4 whose version 0 movie header records duration.
func MP4(duration time.Duration) []byte {
	return slices.Concat(
		Box("ftyp", []byte("isom\x00\x00\x02\x00isom")),
		// The timescale counts milliseconds.
		Box("moov", MVHD(1000, uint32(duration.Milliseconds()))), //nolint:gosec // Test durations fit in 49 days of milliseconds.
		Box("mdat", []byte("synthetic frames")),
	)
}
