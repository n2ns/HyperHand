package main

import (
	"bytes"
	"encoding/binary"
)

// icon builds a 16x16 32-bit .ico: a blue square with a white "H".
func icon() []byte {
	const n = 16
	var b bytes.Buffer
	le := func(v ...any) {
		for _, x := range v {
			binary.Write(&b, binary.LittleEndian, x)
		}
	}
	size := uint32(40 + n*n*4 + n*4)
	le(uint16(0), uint16(1), uint16(1))                                                 // ICONDIR
	le(uint8(n), uint8(n), uint8(0), uint8(0), uint16(1), uint16(32), size, uint32(22)) // ICONDIRENTRY
	le(uint32(40), int32(n), int32(2*n), uint16(1), uint16(32), uint32(0), uint32(0), int32(0), int32(0), uint32(0), uint32(0))
	for y := n - 1; y >= 0; y-- { // bottom-up BGRA
		for x := range n {
			h := y >= 3 && y <= 12 && (x == 4 || x == 5 || x == 10 || x == 11 || ((y == 7 || y == 8) && x > 5 && x < 10))
			if h {
				le(uint32(0xFFFFFFFF))
			} else {
				le(uint32(0xFF1E64C8))
			}
		}
	}
	b.Write(make([]byte, n*4)) // AND mask
	return b.Bytes()
}
