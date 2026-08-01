// Package abogus implements the Douyin A-Bogus v1 signer, ported faithfully
// from src/network/douyin_abogus.py (class DouyinABogusSigner).
//
// The signer binds an a_bogus token to the URL query, User-Agent, timing and a
// compact browser-environment payload. The full signature is non-deterministic
// because it consumes wall-clock time and randomness; the SM3 core, RC4,
// base64-like alphabet transform and payload assembly are deterministic and are
// covered by tests. Time and randomness are injectable via the Signer fields so
// a future end-to-end golden vector can pin them.
package abogus

import (
	"math"
	"math/rand"
	"net/url"
	"sort"
	"strings"

	"fubao.ccvar.com/engine/internal/sign/cryptoutil"
)

// SignedUserAgent is the User-Agent the signature is bound to.
const SignedUserAgent = "Mozilla/5.0 (Windows NT 10.0; Win64; x64) " +
	"AppleWebKit/537.36 (KHTML, like Gecko) " +
	"Chrome/123.0.0.0 Safari/537.36"

const (
	defaultBrowser = "1536|747|1536|834|0|30|0|0|1536|834|1536|864|1525|747|24|24|Win32"
	endString      = "cus"
	aid            = 6383
	pageID         = 6241
	alphabet       = "Dkdpgh2ZmsQB80/MfvV36XI1R45-WUAlEixNLwoqYTOPuzKFjJnry79HbGcaStCe"
	alphabetS3     = "ckdp1h4ZKsUB80/Mfvw36XIgR25+WQAlEi7NLboqYTOPuzmFjJnryx9HVGDaStCe"
)

var arguments = [3]int{0, 1, 14}

// Signer is a Douyin A-Bogus signer. It is safe to reuse across calls.
// NowFunc and Rand are injectable seams mirroring the Python call sites; when
// nil, real wall-clock time and math/rand are used.
type Signer struct {
	Browser     string
	browserCode []int
	browserLen  int

	// NowFunc returns the current time in milliseconds, mirroring the Python
	// int(time() * 1000). Called exactly once per SignParams.
	NowFunc func() int64
	// RandInt returns a random integer in [lo, hi] inclusive, mirroring the
	// Python randint(4, 8). Called exactly once per SignParams (with 4, 8).
	RandInt func(lo, hi int) int
	// RandFloat returns a random float in [0, 1), mirroring Python random().
	// Called by the prefix generator: three _random_list calls, each consuming
	// one random() value, in order -> 3 calls per SignParams.
	RandFloat func() float64
}

// NewSigner returns a Signer for the given browser environment string. An empty
// browser selects the default Windows/Chrome environment.
func NewSigner(browser string) *Signer {
	if browser == "" {
		browser = defaultBrowser
	}
	code := make([]int, 0, len(browser))
	for _, r := range browser {
		code = append(code, int(r))
	}
	return &Signer{
		Browser:     browser,
		browserCode: code,
		browserLen:  len([]rune(browser)),
	}
}

func (s *Signer) now() int64 {
	if s.NowFunc != nil {
		return s.NowFunc()
	}
	return timeNowMillis()
}

func (s *Signer) randInt(lo, hi int) int {
	if s.RandInt != nil {
		return s.RandInt(lo, hi)
	}
	return lo + rand.Intn(hi-lo+1)
}

func (s *Signer) randFloat() float64 {
	if s.RandFloat != nil {
		return s.RandFloat()
	}
	return rand.Float64()
}

// SignParams computes the a_bogus token for the given params and HTTP method.
// params may be a map[string]string or a raw query string.
func (s *Signer) SignParams(params interface{}, method string) string {
	if method == "" {
		method = "GET"
	}
	query := s.queryString(params)
	return s.getValue(query, method)
}

// WithSignature returns a copy of params (a_bogus stripped and recomputed) with
// a fresh a_bogus appended. The input is never mutated.
func (s *Signer) WithSignature(params interface{}, method string) map[string]string {
	signed := map[string]string{}
	switch p := params.(type) {
	case string:
		for _, part := range strings.Split(p, "&") {
			if !strings.Contains(part, "=") {
				continue
			}
			kv := strings.SplitN(part, "=", 2)
			if kv[0] == "a_bogus" {
				continue
			}
			signed[kv[0]] = kv[1]
		}
	case map[string]string:
		for k, v := range p {
			if k == "a_bogus" {
				continue
			}
			signed[k] = v
		}
	}
	signed["a_bogus"] = s.SignParams(signed, method)
	return signed
}

// queryString mirrors _query_string: raw strings pass through, maps are
// urlencoded (a_bogus stripped) with keys sorted like Python's urlencode over a
// dict that dropped a_bogus.
func (s *Signer) queryString(params interface{}) string {
	switch p := params.(type) {
	case string:
		return p
	case map[string]string:
		keys := make([]string, 0, len(p))
		for k := range p {
			if k == "a_bogus" {
				continue
			}
			keys = append(keys, k)
		}
		sort.Strings(keys)
		var b strings.Builder
		for i, k := range keys {
			if i > 0 {
				b.WriteByte('&')
			}
			b.WriteString(url.QueryEscape(k))
			b.WriteByte('=')
			b.WriteString(url.QueryEscape(p[k]))
		}
		return b.String()
	}
	return ""
}

func (s *Signer) getValue(query, method string) string {
	now := s.now()
	end := now + int64(s.randInt(4, 8))
	encrypted := s.encryptedPayload(query, method, now, end)
	return baseLike(s.randomPrefix()+encrypted, "s4") + "="
}

// randomList mirrors _random_list. seed==0 means "use random()*10000".
func (s *Signer) randomList(seed float64, b, c, d, e, f, g int) []int {
	value := seed
	if value == 0 {
		value = s.randFloat() * 10000
	}
	iv := int(value)
	p1 := iv & 255
	p2 := iv >> 8
	return []int{
		p1&b | d,
		p1&c | e,
		p2&b | f,
		p2&c | g,
	}
}

func (s *Signer) randomPrefix() string {
	var values []int
	values = append(values, s.randomList(0, 170, 85, 1, 2, 5, 45&170)...)
	values = append(values, s.randomList(0, 170, 85, 1, 0, 0, 0)...)
	values = append(values, s.randomList(0, 170, 85, 1, 0, 5, 0)...)
	var b strings.Builder
	for _, code := range values {
		b.WriteRune(rune(code))
	}
	return b.String()
}

func (s *Signer) encryptedPayload(query, method string, startTime, endTime int64) string {
	paramsArray := cryptoutil.DoubleSM3(query + endString)
	suffixArray := cryptoutil.DoubleSM3(endString)

	rc4Key := runesToString([]int{0, 1, arguments[2]})
	uaArray := sm3ArrayFromString(baseLike(rc4(SignedUserAgent, rc4Key), "s3"))

	payload := s.payloadList(startTime, endTime, paramsArray, suffixArray, uaArray)
	return rc4(runesToString(payload), "y")
}

func (s *Signer) payloadList(startTime, endTime int64, paramsArray, suffixArray, uaArray []int) []int {
	a := arguments
	envLen := s.browserLen
	emptyLen := 0

	st := int(startTime)
	et := int(endTime)

	fields := []int{
		44,
		(st >> 24) & 255,
		(pageID >> 24) & 255,
		(a[0] >> 24) & 255,
		(a[1] / 256) & 255,
		(a[2] >> 24) & 255,
		(aid >> 8) & 255,
		paramsArray[21],
		suffixArray[21],
		(pageID >> 16) & 255,
		uaArray[23],
		(st >> 16) & 255,
		(a[0] >> 16) & 255,
		(pageID >> 8) & 255,
		pageID & 255,
		a[1] % 256,
		(a[2] >> 16) & 255,
		aid & 255,
		paramsArray[22],
		suffixArray[22],
		uaArray[24],
		(st >> 8) & 255,
		(a[0] >> 8) & 255,
		(a[1] >> 24) & 255,
		(aid >> 24) & 255,
		(a[2] >> 8) & 255,
		st & 255,
		a[0] & 255,
		(a[1] >> 16) & 255,
		a[2] & 255,
		(et >> 24) & 255,
		(et >> 16) & 255,
		(aid >> 16) & 255,
		(et >> 8) & 255,
		et & 255,
		3,
		divPow(et, 4),
		divPow(et, 5),
		divPow(st, 4),
		divPow(st, 5),
		envLen & 255,
		(envLen >> 8) & 255,
		emptyLen & 255,
		(emptyLen >> 8) & 255,
	}

	checksum := 0
	for _, v := range fields {
		checksum ^= v
	}

	payload := make([]int, 0, len(fields)+len(s.browserCode)+1)
	payload = append(payload, fields...)
	payload = append(payload, s.browserCode...)
	payload = append(payload, checksum)

	out := make([]int, len(payload))
	for i, v := range payload {
		out[i] = v & 255
	}
	return out
}

// divPow mirrors Python's int(value / 256**n) >> 0. Because start/end times are
// millisecond timestamps that fit in float64 exactly, this equals value / 256**n
// with truncation toward zero.
func divPow(value, n int) int {
	d := 1
	for i := 0; i < n; i++ {
		d *= 256
	}
	return value / d
}

// sm3ArrayFromString mirrors _sm3_array(str) using cryptoutil.
func sm3ArrayFromString(v string) []int {
	return cryptoutil.SM3Array(v)
}

// runesToString mirrors "".join(chr(item) for item in seq): each int becomes a
// rune (code point). Callers pass values in [0,255] but code points up to the
// alphabet index range are handled via rune encoding.
func runesToString(seq []int) string {
	var b strings.Builder
	for _, c := range seq {
		b.WriteRune(rune(c))
	}
	return b.String()
}

// rc4 mirrors _rc4: keystream XORed against code points of plaintext, producing
// a string of code points.
func rc4(plaintext, key string) string {
	pt := []rune(plaintext)
	k := []rune(key)

	state := make([]int, 256)
	for i := range state {
		state[i] = i
	}
	j := 0
	for i := 0; i < 256; i++ {
		j = (j + state[i] + int(k[i%len(k)])) % 256
		state[i], state[j] = state[j], state[i]
	}

	i, j := 0, 0
	out := make([]rune, 0, len(pt))
	for _, ch := range pt {
		i = (i + 1) % 256
		j = (j + state[i]) % 256
		state[i], state[j] = state[j], state[i]
		idx := (state[i] + state[j]) % 256
		out = append(out, rune(state[idx]^int(ch)))
	}
	return string(out)
}

// baseLike mirrors _base64_like: a custom base64 alphabet transform over the
// code points of value. alphabetKey is "s3" or "s4".
func baseLike(value, alphabetKey string) string {
	ab := alphabet
	if alphabetKey == "s3" {
		ab = alphabetS3
	}
	v := []rune(value)
	n := len(v)
	limit := int(math.Ceil(float64(n) / 3 * 4))
	var b strings.Builder
	for outputIndex := 0; outputIndex < limit; outputIndex++ {
		roundIndex := (outputIndex / 4) * 3
		var acc int
		if roundIndex < n {
			acc |= int(v[roundIndex]) << 16
		}
		if roundIndex+1 < n {
			acc |= int(v[roundIndex+1]) << 8
		}
		if roundIndex+2 < n {
			acc |= int(v[roundIndex+2])
		}
		switch outputIndex % 4 {
		case 0:
			b.WriteByte(ab[(acc&0xFC0000)>>18])
		case 1:
			b.WriteByte(ab[(acc&0x03F000)>>12])
		case 2:
			b.WriteByte(ab[(acc&0x0FC0)>>6])
		default:
			b.WriteByte(ab[acc&0x3F])
		}
	}
	return b.String()
}

// SignDouyinParams mirrors the module-level sign_douyin_params: sign with a
// default signer and return the params map with a_bogus attached.
func SignDouyinParams(params interface{}) map[string]string {
	return NewSigner("").WithSignature(params, "GET")
}
