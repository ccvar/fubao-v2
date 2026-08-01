// Package cryptoutil implements the standard SM3 cryptographic hash
// (GM/T 0004-2012) in pure Go, ported to match Python's hashlib.new("sm3").
package cryptoutil

import (
	"encoding/binary"
	"math/bits"
)

var sm3IV = [8]uint32{
	0x7380166f, 0x4914b2b9, 0x172442d7, 0xda8a0600,
	0xa96f30bc, 0x163138aa, 0xe38dee4d, 0xb0fb0e4e,
}

func sm3T(j int) uint32 {
	if j < 16 {
		return 0x79cc4519
	}
	return 0x7a879d8a
}

func ff(x, y, z uint32, j int) uint32 {
	if j < 16 {
		return x ^ y ^ z
	}
	return (x & y) | (x & z) | (y & z)
}

func gg(x, y, z uint32, j int) uint32 {
	if j < 16 {
		return x ^ y ^ z
	}
	return (x & y) | (^x & z)
}

func p0(x uint32) uint32 {
	return x ^ bits.RotateLeft32(x, 9) ^ bits.RotateLeft32(x, 17)
}

func p1(x uint32) uint32 {
	return x ^ bits.RotateLeft32(x, 15) ^ bits.RotateLeft32(x, 23)
}

// SM3 computes the standard SM3 digest of data.
func SM3(data []byte) [32]byte {
	// Padding.
	msgLen := uint64(len(data)) * 8
	padded := make([]byte, len(data))
	copy(padded, data)
	padded = append(padded, 0x80)
	for len(padded)%64 != 56 {
		padded = append(padded, 0x00)
	}
	var lenBytes [8]byte
	binary.BigEndian.PutUint64(lenBytes[:], msgLen)
	padded = append(padded, lenBytes[:]...)

	v := sm3IV
	for off := 0; off < len(padded); off += 64 {
		block := padded[off : off+64]
		var w [68]uint32
		for i := 0; i < 16; i++ {
			w[i] = binary.BigEndian.Uint32(block[i*4 : i*4+4])
		}
		for i := 16; i < 68; i++ {
			w[i] = p1(w[i-16]^w[i-9]^bits.RotateLeft32(w[i-3], 15)) ^
				bits.RotateLeft32(w[i-13], 7) ^ w[i-6]
		}
		var w1 [64]uint32
		for i := 0; i < 64; i++ {
			w1[i] = w[i] ^ w[i+4]
		}

		a, b, c, d := v[0], v[1], v[2], v[3]
		e, f, g, h := v[4], v[5], v[6], v[7]
		for j := 0; j < 64; j++ {
			ss1 := bits.RotateLeft32(bits.RotateLeft32(a, 12)+e+bits.RotateLeft32(sm3T(j), j%32), 7)
			ss2 := ss1 ^ bits.RotateLeft32(a, 12)
			tt1 := ff(a, b, c, j) + d + ss2 + w1[j]
			tt2 := gg(e, f, g, j) + h + ss1 + w[j]
			d = c
			c = bits.RotateLeft32(b, 9)
			b = a
			a = tt1
			h = g
			g = bits.RotateLeft32(f, 19)
			f = e
			e = p0(tt2)
		}
		v[0] ^= a
		v[1] ^= b
		v[2] ^= c
		v[3] ^= d
		v[4] ^= e
		v[5] ^= f
		v[6] ^= g
		v[7] ^= h
	}

	var out [32]byte
	for i := 0; i < 8; i++ {
		binary.BigEndian.PutUint32(out[i*4:i*4+4], v[i])
	}
	return out
}

// SM3Array returns SM3(utf8(s)) as a slice of 32 ints (0-255).
func SM3Array(s string) []int {
	d := SM3([]byte(s))
	out := make([]int, 32)
	for i, b := range d {
		out[i] = int(b)
	}
	return out
}

// DoubleSM3 returns SM3(SM3(utf8(s))) as a slice of 32 ints (0-255),
// matching Python's _double_sm3 = _sm3_array(_sm3_array(value)).
func DoubleSM3(s string) []int {
	inner := SM3([]byte(s))
	d := SM3(inner[:])
	out := make([]int, 32)
	for i, b := range d {
		out[i] = int(b)
	}
	return out
}
