package dashboard

import "crypto/subtle"

// subtleEqual 常量时间比较，避免令牌校验的时序侧信道。
func subtleEqual(a, b string) bool {
	return subtle.ConstantTimeCompare([]byte(a), []byte(b)) == 1
}
