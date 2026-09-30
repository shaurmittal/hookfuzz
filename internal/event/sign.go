package event

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
)

// Sign returns a Stripe-Signature header value for payload, signed at unix
// time t: "t=<t>,v1=<hex HMAC-SHA256 of "<t>.<payload>">".
func Sign(payload []byte, secret string, t int64) string {
	mac := hmac.New(sha256.New, []byte(secret))
	fmt.Fprintf(mac, "%d.", t)
	mac.Write(payload)
	return fmt.Sprintf("t=%d,v1=%s", t, hex.EncodeToString(mac.Sum(nil)))
}
