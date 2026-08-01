package v2

import "math"

// _JsSM3 is a NON-STANDARD SM3 reverse-engineered from JavaScript. It
// deliberately does NOT equal OpenSSL/standard SM3. Ported line-by-line from
// the Python reference (src/network/douyin_abogus_v2.py).

type jsSM3 struct {
	chunk []int
	reg   [8]uint32
	size  int
}

func newJSSM3() *jsSM3 {
	return &jsSM3{
		reg: [8]uint32{
			0x7380166F,
			0x4914B2B9,
			0x172442D7,
			0xDA8A0600,
			0xA96F30BC,
			0x163138AA,
			0xE38DEE4D,
			0xB0FB0E4E,
		},
	}
}

// sm3Array mirrors module-level _sm3_array: a fresh digest of the input.
func sm3Array(value []int) []int {
	return newJSSM3().writeAndDigest(value)
}

func sm3ArrayStr(value string) []int {
	return newJSSM3().writeAndDigest(encodeUTF8Surrogatepass(value))
}

func (s *jsSM3) writeAndDigest(encoded []int) []int {
	s.write(encoded)
	s.fill()
	for index := 0; index < len(s.chunk); index += 64 {
		s.compress(s.chunk[index : index+64])
	}
	result := make([]int, 0, 32)
	for _, item := range s.reg {
		result = append(result,
			int((item>>24)&255),
			int((item>>16)&255),
			int((item>>8)&255),
			int(item&255),
		)
	}
	return result
}

func (s *jsSM3) write(encoded []int) {
	s.size += len(encoded)
	take := 64 - len(s.chunk)
	if len(encoded) < take {
		s.chunk = append(s.chunk, encoded...)
		return
	}

	s.chunk = append(s.chunk, encoded[:take]...)
	for len(s.chunk) >= 64 {
		s.compress(s.chunk[:64])
		if take < len(encoded) {
			end := take + 64
			if end > len(encoded) {
				end = len(encoded)
			}
			s.chunk = append([]int(nil), encoded[take:end]...)
		} else {
			s.chunk = nil
		}
		take += 64
	}
}

func (s *jsSM3) fill() {
	totalBits := s.size * 8
	highBits := (int(math.Floor(float64(totalBits) / 0x100000000))) & 0xFFFFFFFF
	lowBits := totalBits & 0xFFFFFFFF
	s.chunk = append(s.chunk, 0x80)
	if 64-(len(s.chunk)%64) < 8 {
		for len(s.chunk)%64 != 0 {
			s.chunk = append(s.chunk, 0)
		}
	}
	for len(s.chunk)%64 != 56 {
		s.chunk = append(s.chunk, 0)
	}
	for index := 0; index < 4; index++ {
		s.chunk = append(s.chunk, (highBits>>(8*(3-index)))&255)
	}
	for index := 0; index < 4; index++ {
		s.chunk = append(s.chunk, (lowBits>>(8*(3-index)))&255)
	}
}

func (s *jsSM3) compress(block []int) {
	if len(block) < 64 {
		panic("compress error: not enough data")
	}
	var words [132]uint32
	for index := 0; index < 16; index++ {
		words[index] = u32(uint32(block[4*index])<<24 |
			uint32(block[4*index+1])<<16 |
			uint32(block[4*index+2])<<8 |
			uint32(block[4*index+3]))
	}
	for index := 16; index < 68; index++ {
		tmp := words[index-16] ^ words[index-9] ^ rol32(words[index-3], 15)
		tmp = tmp ^ rol32(tmp, 15) ^ rol32(tmp, 23)
		words[index] = u32(tmp ^ rol32(words[index-13], 7) ^ words[index-6])
	}
	for index := 0; index < 64; index++ {
		words[index+68] = u32(words[index] ^ words[index+4])
	}

	a, b, c, d := s.reg[0], s.reg[1], s.reg[2], s.reg[3]
	e, f, g, h := s.reg[4], s.reg[5], s.reg[6], s.reg[7]
	for index := 0; index < 64; index++ {
		ss1 := rol32(u32(rol32(a, 12)+e+rol32(sm3T(index), uint(index))), 7)
		ss2 := ss1 ^ rol32(a, 12)
		tt1 := u32(sm3FF(index, a, b, c) + d + ss2 + words[index+68])
		tt2 := u32(sm3GG(index, e, f, g) + h + ss1 + words[index])
		d = c
		c = rol32(b, 9)
		b = a
		a = tt1
		h = g
		g = rol32(f, 19)
		e = tt2
		// NOTE: the JS-derived routine deliberately never updates `f`, unlike
		// standard SM3. Do not add `f = e` here.
	}

	s.reg[0] = u32(s.reg[0] ^ a)
	s.reg[1] = u32(s.reg[1] ^ b)
	s.reg[2] = u32(s.reg[2] ^ c)
	s.reg[3] = u32(s.reg[3] ^ d)
	s.reg[4] = u32(s.reg[4] ^ e)
	s.reg[5] = u32(s.reg[5] ^ f)
	s.reg[6] = u32(s.reg[6] ^ g)
	s.reg[7] = u32(s.reg[7] ^ h)
}

func u32(value uint32) uint32 {
	return value & 0xFFFFFFFF
}

func rol32(value uint32, shift uint) uint32 {
	shift %= 32
	value = u32(value)
	if shift == 0 {
		return value
	}
	return u32((value << shift) | (value >> (32 - shift)))
}

func sm3T(index int) uint32 {
	if index < 16 {
		return 0x79CC4519
	}
	return 0x7A879D8A
}

func sm3FF(index int, x, y, z uint32) uint32 {
	if index < 16 {
		return u32(x ^ y ^ z)
	}
	return u32((x & y) | (x & z) | (y & z))
}

func sm3GG(index int, x, y, z uint32) uint32 {
	if index < 16 {
		return u32(x ^ y ^ z)
	}
	return u32((x & y) | (u32(^x) & z))
}
