package v2

// encodeUTF8Surrogatepass mirrors Python's
// value.encode("utf-8", errors="surrogatepass") applied to a Go string that we
// treat as a sequence of Unicode code points ([]rune). Lone surrogate code
// points (U+D800..U+DFFF) are encoded as their raw 3-byte CESU-style form
// rather than replaced, matching CPython's surrogatepass handler.
func encodeUTF8Surrogatepass(value string) []int {
	return encodeRunesSurrogatepass([]rune(value))
}

func encodeRunesSurrogatepass(runes []rune) []int {
	out := make([]int, 0, len(runes))
	for _, r := range runes {
		cp := uint32(r)
		switch {
		case cp < 0x80:
			out = append(out, int(cp))
		case cp < 0x800:
			out = append(out,
				int(0xC0|(cp>>6)),
				int(0x80|(cp&0x3F)),
			)
		case cp < 0x10000:
			// Includes the surrogate range U+D800..U+DFFF, which
			// surrogatepass encodes as a raw 3-byte sequence.
			out = append(out,
				int(0xE0|(cp>>12)),
				int(0x80|((cp>>6)&0x3F)),
				int(0x80|(cp&0x3F)),
			)
		default:
			out = append(out,
				int(0xF0|(cp>>18)),
				int(0x80|((cp>>12)&0x3F)),
				int(0x80|((cp>>6)&0x3F)),
				int(0x80|(cp&0x3F)),
			)
		}
	}
	return out
}
