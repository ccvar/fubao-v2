// Package v2 implements the SM3-based A-Bogus v2 signer (x-common-params-v2)
// for Douyin web APIs. It is a faithful port of
// src/network/douyin_abogus_v2.py and is self-contained (it does not import
// cryptoutil): its SM3 is the non-standard JS variant, not standard SM3.
package v2

import (
	"math"
	"strconv"
	"strings"
	"time"
)

// ABOGUSV2UserAgent is the default user agent for v2 signing.
const ABOGUSV2UserAgent = "Mozilla/5.0 (Windows NT 10.0; Win64; x64) " +
	"AppleWebKit/537.36 (KHTML, like Gecko) " +
	"Chrome/132.0.0.0 Safari/537.36"

const (
	finalAlphabet = "Dkdpgh2ZmsQB80/MfvV36XI1R45-WUAlEixNLwoqYTOPuzKFjJnry79HbGcaStCe"
	uaAlphabet    = "ckdp1h4ZKsUB80/Mfvw36XIgR25+WQAlEi7NLboqYTOPuzmFjJnryx9HVGDaStCe"
)

var abTable = []int{
	120, 225, 52, 163, 232, 86, 209, 17, 59, 145, 33, 195, 44, 13, 174, 103,
	152, 66, 249, 42, 187, 99, 222, 7, 180, 118, 22, 89, 155, 204, 74, 131,
	53, 202, 19, 141, 56, 217, 31, 125, 67, 92, 238, 107, 26, 61, 111, 79,
	191, 243, 39, 168, 246, 2, 47, 170, 97, 114, 213, 84, 50, 16, 77, 189,
	136, 38, 116, 176, 70, 248, 159, 171, 24, 142, 198, 102, 55, 165, 228,
	93, 147, 127, 201, 48, 207, 83, 162, 215, 230, 54, 100, 181, 40, 160,
	196, 185, 94, 251, 78, 193, 9, 65, 134, 232, 0, 149, 212, 88, 60, 240,
	105, 81, 254, 124, 45, 204, 140, 166, 218, 182, 109, 37, 173, 98, 121,
	235, 237, 76, 253, 178, 150, 123, 11, 255, 153, 184, 31, 226, 156, 206,
	138, 220, 95, 245, 82, 223, 192, 112, 46, 199, 241, 157, 164, 128, 214,
	167, 204, 98, 3, 129, 58, 139, 233, 110, 12, 101, 72, 167, 34, 18, 153,
	222, 140, 250, 119, 132, 158, 154, 101, 43, 172, 188, 73, 29, 126, 224,
	232, 234, 23, 241, 180, 137, 232, 28, 228, 62, 183, 203, 210, 154, 232,
	57, 239, 175, 144, 232, 41, 250, 104, 232, 113, 143,
}

var uaTable = []int{
	59, 225, 120, 232, 52, 163, 17, 86, 145, 209, 33, 59, 13, 174, 44, 103,
	66, 152, 42, 249, 187, 99, 222, 7, 180, 118, 22, 89, 155, 204, 74, 131,
	53, 202, 19, 141, 56, 217, 31, 125, 67, 92, 238, 107, 26, 61, 111, 79,
	191, 243, 39, 168, 246, 2, 47, 170, 97, 114, 213, 84, 50, 16, 77, 189,
	136, 38, 116, 176, 70, 248, 159, 171, 24, 142, 198, 102, 55, 165, 228,
	93, 147, 127, 201, 48, 207, 83, 162, 215, 230, 54, 100, 181, 40, 160,
	196, 185, 94, 251, 78, 193, 9, 65, 134, 232, 0, 149, 212, 88, 60, 240,
	105, 81, 254, 124, 45, 204, 140, 166, 218, 182, 109, 37, 173, 98, 121,
	235, 237, 76, 253, 178, 150, 123, 11, 255, 153, 184, 31, 226, 156, 206,
	138, 220, 95, 245, 82, 223, 192, 112, 46, 199, 241, 157, 164, 128, 214,
	167, 204, 98, 3, 129, 58, 139, 233, 110, 12, 101, 72, 167, 34, 18, 153,
	222, 140, 250, 119, 132, 158, 154, 101, 43, 172, 188, 73, 29, 126, 224,
	232, 234, 23, 241, 180, 137, 232, 28, 228, 62, 183, 203, 210, 154, 232,
	57, 239, 175, 144, 232, 41, 250, 104, 232, 113, 143,
}

// Signer is the v2 A-Bogus signer with injectable clock and RNG.
type Signer struct {
	UserAgent string
	NowMs     int64
	Random    func() float64
}

// NewSigner builds a Signer. If random is nil, a deterministic zero RNG is
// used; if ua is empty the default v2 user agent is used. NowMs of 0 falls
// back to the wall clock at sign time.
func NewSigner(ua string, nowMs int64, random func() float64) *Signer {
	if ua == "" {
		ua = ABOGUSV2UserAgent
	}
	if random == nil {
		random = func() float64 { return 0 }
	}
	return &Signer{UserAgent: ua, NowMs: nowMs, Random: random}
}

// nowMsProvided reports whether NowMs was explicitly set (non-zero), mirroring
// the Python `now_ms is not None` semantics. A caller wanting the wall clock
// passes 0.
func (s *Signer) hasNowMs() bool { return s.NowMs != 0 }

func (s *Signer) currentMs() int64 {
	if s.hasNowMs() {
		return s.NowMs
	}
	return time.Now().UnixMilli()
}

// Sign returns the SM3 A-Bogus variant used by x-common-params-v2. A nil data
// pointer takes the "null" path.
func (s *Signer) Sign(url string, data *string) string {
	query := url
	if idx := strings.Index(url, "?"); idx >= 0 {
		query = url[idx+1:]
	}
	params := query + "dhzx"
	dataStr := "null"
	if data != nil {
		dataStr = *data
	}
	garbled := s.garbledString(params, dataStr+"dhzx")
	return finalBase64(garbled)
}

func (s *Signer) garbledString(params, data string) []rune {
	timestamp1 := s.currentMs()
	timestamp2 := timestamp1 - int64(math.Floor(s.Random()*10))
	arr29 := s.arr29(timestamp1, timestamp2, params, data)
	prefix := s.topHeaderRandomGarbledCharacters()
	arr29Str := make([]rune, 0, len(arr29))
	for _, item := range arr29 {
		arr29Str = append(arr29Str, fromCharCode(item))
	}
	result := make([]rune, 0, len(prefix)+len(arr29))
	result = append(result, prefix...)
	result = append(result, s.abGarbledCharacters(arr29Str)...)
	return result
}

func (s *Signer) arr29(timestamp1, timestamp2 int64, params, data string) []int {
	parArr := sm3Array(sm3ArrayStr(params))
	dataArr := sm3Array(sm3ArrayStr(data))
	uaSalt := 0
	browserArr := sm3ArrayStr(s.encryptionUA(s.uaGarbledCharacters(s.UserAgent, uaSalt)))

	arr := make([]int, 55)
	uaCodes := []rune(s.UserAgent)

	nowForDate := s.currentMs()
	dateTime3 := int(math.Floor(float64(nowForDate-1721836800000) / 1000 / 60 / 60 / 24 / 14))
	randomArr := s.randomGarbledCharactersArrayList()

	arr[0] = 41
	arr[1] = dateTime3
	arr[2] = 5
	arr[3] = int(timestamp1-timestamp2+3) & 255

	dt1 := timestamp1
	arr[4] = int(dt1>>0) & 255
	arr[5] = int(dt1>>8) & 255
	arr[6] = int(dt1>>16) & 255
	arr[7] = int(dt1>>24) & 255
	arr[8] = int(math.Floor(float64(dt1)/math.Pow(256, 4))) & 255
	arr[9] = int(math.Floor(float64(dt1)/math.Pow(256, 5))) & 255

	arr[10] = 1 & 255
	arr[11] = int(math.Floor(1.0/256)) & 255
	arr[12] = 1 & 255
	arr[13] = (1 >> 8) & 255
	arr[14] = 1
	arr[15] = 0
	arr[16] = 0
	arr[17] = 0

	arr[18] = uaSalt & 255
	arr[19] = (uaSalt >> 8) & 255
	arr[20] = (uaSalt >> 16) & 255
	arr[21] = (uaSalt >> 24) & 255

	arr[22] = parArr[9]
	arr[23] = parArr[18]
	arr[24] = 3
	arr[25] = parArr[3]

	arr[26] = dataArr[10]
	arr[27] = dataArr[19]
	arr[28] = 4
	arr[29] = dataArr[4]

	arr[30] = browserArr[11]
	arr[31] = browserArr[21]
	arr[32] = 5
	arr[33] = browserArr[5]

	dt2 := timestamp2
	arr[34] = int(dt2>>0) & 255
	arr[35] = int(dt2>>8) & 255
	arr[36] = int(dt2>>16) & 255
	arr[37] = int(dt2>>24) & 255
	arr[38] = int(math.Floor(float64(dt2)/math.Pow(256, 4))) & 255
	arr[39] = int(math.Floor(float64(dt2)/math.Pow(256, 5))) & 255
	arr[40] = 3

	arr32 := 6241
	arr[41] = (arr32 >> 0) & 255
	arr[42] = (arr32 >> 8) & 255
	arr[43] = (arr32 >> 16) & 255
	arr[44] = (arr32 >> 24) & 255

	arr36 := 6383
	arr[45] = arr36 & 255
	arr[46] = (arr36 >> 8) & 255
	arr[47] = (arr36 >> 16) & 255
	arr[48] = (arr36 >> 24) & 255

	lastNumOne := last3Num(timestamp1)
	arr[49] = len(uaCodes)
	arr[50] = len(uaCodes) & 255
	arr[51] = (len(uaCodes) >> 8) & 255
	arr[52] = len(lastNumOne)
	arr[53] = len(lastNumOne) & 255
	arr[54] = (len(lastNumOne) >> 8) & 255

	lastNum := lastNum2(randomArr, arr)
	arr2 := []int{
		arr[9], arr[18], arr[30], arr[35], arr[47], arr[4], arr[44], arr[19], arr[10], arr[23],
		arr[12], arr[40], arr[25], arr[42], arr[3], arr[22], arr[38], arr[21], arr[5], arr[45],
		arr[1], arr[29], arr[6], arr[43], arr[33], arr[14], arr[36], arr[37], arr[2], arr[46],
		arr[15], arr[48], arr[31], arr[26], arr[16], arr[13], arr[8], arr[41], arr[27], arr[17],
		arr[39], arr[20], arr[11], arr[0], arr[34], arr[7], arr[50], arr[51], arr[53], arr[54],
	}

	values := make([]numVal, 0, len(arr2)+len(uaCodes)+len(lastNumOne)+1)
	for _, v := range arr2 {
		values = append(values, numVal{n: v})
	}
	for _, r := range uaCodes {
		values = append(values, numVal{n: int(r)})
	}
	for _, v := range lastNumOne {
		values = append(values, numVal{n: v})
	}
	values = append(values, lastNum)
	return numList(randomArr, values)
}

func (s *Signer) abGarbledCharacters(text []rune) []rune {
	result := make([]rune, 0, len(text))
	for index, char := range text {
		result = append(result, fromCharCode(tableValue(abTable, (int(char)+index)&255)))
	}
	return result
}

func (s *Signer) uaGarbledCharacters(userAgent string, _salt int) string {
	runes := []rune(userAgent)
	var b strings.Builder
	for index, char := range runes {
		b.WriteRune(fromCharCode(tableValue(uaTable, (int(char)+index)&255)))
	}
	return b.String()
}

func (s *Signer) encryptionUA(text string) string {
	runes := []rune(text)
	var out strings.Builder
	j := 0
	i := 0
	for i < len(runes) {
		if i+3 <= len(runes) {
			n0 := int(runes[i]) & 255
			n1 := int(runes[i+1]) & 255
			n2 := int(runes[i+2]) & 255
			baseNum := (n0 << 16) | (n1 << 8) | n2
			out.WriteByte(uaAlphabet[(baseNum&16515072)>>18])
			out.WriteByte(uaAlphabet[(baseNum&258048)>>12])
			out.WriteByte(uaAlphabet[(baseNum&4032)>>6])
			out.WriteByte(uaAlphabet[baseNum&63])
		} else {
			remain := len(runes) - i
			if remain == 2 {
				n0 := int(runes[j]) & 255
				n1 := int(runes[j+1]) & 255
				baseNum := (n0 << 16) | (n1 << 8)
				out.WriteByte(uaAlphabet[(baseNum&16515072)>>18])
				out.WriteByte(uaAlphabet[(baseNum&258048)>>12])
				out.WriteByte(uaAlphabet[(baseNum&4032)>>6])
				out.WriteByte('=')
			} else if remain == 1 {
				n0 := int(runes[j]) & 255
				baseNum := n0 << 16
				out.WriteByte(uaAlphabet[(baseNum&16515072)>>18])
				out.WriteByte(uaAlphabet[(baseNum&258048)>>12])
				out.WriteByte('=')
				out.WriteByte('=')
			}
		}
		j += 3
		i += 4
	}
	return out.String()
}

func (s *Signer) randomGarbledCharactersArrayList() []int {
	out := make([]int, 16)
	for k := 0; k < 16; k++ {
		out[k] = int(math.Floor(s.Random() * 256))
	}
	return out
}

func (s *Signer) topHeaderRandomGarbledCharacters() []rune {
	const chars = "0123456789ABCDEF"
	out := make([]rune, 32)
	for k := 0; k < 32; k++ {
		out[k] = rune(chars[int(math.Floor(s.Random()*16))])
	}
	return out
}

func finalBase64(text []rune) string {
	var out strings.Builder
	j := 0
	i := 0
	for i <= len(text) {
		if i+3 <= len(text) {
			c0 := int(text[i])
			c1 := int(text[i+1])
			c2 := int(text[i+2])
			baseNum := c2 | (c1 << 8) | (c0 << 16)
			out.WriteByte(finalAlphabet[(baseNum&16515072)>>18])
			out.WriteByte(finalAlphabet[(baseNum&258048)>>12])
			out.WriteByte(finalAlphabet[(baseNum&4032)>>6])
			out.WriteByte(finalAlphabet[baseNum&63])
		}
		if i+3 > len(text) {
			remain := len(text) - j
			if remain == 2 {
				c0 := int(text[j])
				c1 := int(text[j+1])
				baseNum := (c1 << 8) | (c0 << 16)
				out.WriteByte(finalAlphabet[(baseNum&16515072)>>18])
				out.WriteByte(finalAlphabet[(baseNum&258048)>>12])
				out.WriteByte(finalAlphabet[(baseNum&4032)>>6])
				out.WriteByte('=')
			}
			if remain == 1 {
				c0 := int(text[j])
				baseNum := c0 << 16
				out.WriteByte(finalAlphabet[(baseNum&16515072)>>18])
				out.WriteByte(finalAlphabet[(baseNum&258048)>>12])
				out.WriteByte('=')
				out.WriteByte('=')
			}
		}
		j += 3
		i += 4
	}
	return out.String()
}

// listSentinel is the value a Python list element yields through
// _from_char_code ("\x00" == code point 0).
const listSentinel = 0

func tableValue(table []int, index int) int {
	if index >= 0 && index < len(table) {
		return table[index]
	}
	return 0
}

func fromCharCode(value int) rune {
	return rune(value & 0xFFFF)
}

func last3Num(timestamp1 int64) []int {
	v := (int(math.Floor(float64(timestamp1) + 3))) & 255
	text := strconv.Itoa(v) + ","
	out := make([]int, 0, len(text))
	for _, ch := range text {
		out = append(out, int(ch))
	}
	return out
}

// numVal models a Python "values" element that may be either a numeric value
// or a non-numeric list object (the _last_num2 result appended via [last_num]).
// A list element behaves as 0 in _js_bitwise_number and as "\x00" (code point
// 0) when consumed by _from_char_code.
type numVal struct {
	n      int
	isList bool
}

func bitwiseNumber(v numVal) int {
	if v.isList {
		return 0
	}
	return v.n
}

// lastNum2 mirrors Python _last_num2, which returns a list of 16 ints. In the
// caller this whole list is appended as ONE element ([last_num]); its numeric
// contents never contribute, only its list-ness does. We compute it faithfully
// (in case of side-effect-free equivalence) and return a single list-typed
// numVal.
func lastNum2(randomArr []int, arr []int) numVal {
	for i := 0; i < 16; i++ {
		total := 0
		for j := 0; j < 8; j++ {
			total += randomArr[(i+j)%16] * arr[(j*7+i)%55]
		}
		_ = total & 255
	}
	return numVal{isList: true}
}

func numList(randomArr []int, values []numVal) []int {
	output := make([]int, 0, len(values)*2)
	i := 0
	for i < len(values) {
		if i+2 >= len(values) {
			if i+1 >= len(values) {
				output = append(output, valueForOutput(values[i]))
			} else {
				output = append(output, valueForOutput(values[i]))
				output = append(output, valueForOutput(values[i+1]))
			}
		} else {
			first := bitwiseNumber(values[i])
			second := bitwiseNumber(values[i+1])
			third := bitwiseNumber(values[i+2])
			num1 := (first & 192) | (second & 63)
			num2 := ((second&192)|(third&63))<<8 | first
			num3 := (second & 192) | (third & 63) | ((first & 48) << 10)
			num4 := first&255 | ((second & 128) << 1) | ((third & 192) << 2)
			output = append(output, num1, num2, num3, num4)
		}
		i += 3
	}
	out := make([]int, 0, len(randomArr)+len(output))
	out = append(out, randomArr...)
	out = append(out, output...)
	return out
}

// valueForOutput returns the integer a tail element contributes when appended
// verbatim to output. A list element yields the fromCharCode-of-list sentinel;
// _from_char_code returns "\x00" for a non-number, i.e. code point 0.
func valueForOutput(v numVal) int {
	if v.isList {
		return listSentinel
	}
	return v.n
}
