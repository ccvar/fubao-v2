package abogus

import "time"

// timeNowMillis mirrors Python int(time() * 1000).
func timeNowMillis() int64 {
	return time.Now().UnixNano() / int64(time.Millisecond)
}
