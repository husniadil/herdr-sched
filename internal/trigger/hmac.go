package trigger

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"strings"

	"github.com/husniadil/herdr-sched/internal/codes"
)

// SignatureHeader is the header a caller puts the signature in, and
// SignaturePrefix is what names the algorithm inside it. Naming the algorithm
// on the wire is what lets a second one arrive later without a caller having
// to guess which of the two a bare hex string was.
const (
	SignatureHeader = "X-Sched-Signature"
	SignaturePrefix = "sha256="
)

// SecretBytes is how much entropy a webhook secret carries. It is shown ONCE,
// at creation (note 2), so it is generated here rather than chosen by a caller:
// a secret an operator types is a secret an operator can make short.
const SecretBytes = 32

// NewSecret is one webhook's HMAC key, as hex.
func NewSecret() (string, error) {
	var b [SecretBytes]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", codes.Errorf(codes.Unavailable, "draw a webhook secret: %v", err)
	}
	return hex.EncodeToString(b[:]), nil
}

// Sign is the signature a caller sends for one body, header value and all. It
// is here rather than only in a test so that the one place the algorithm is
// written down is the one place both sides read it from.
func Sign(secret string, body []byte) string {
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write(body)
	return SignaturePrefix + hex.EncodeToString(mac.Sum(nil))
}

// Verify holds one request's signature against the body it was sent for.
//
// The comparison is constant-time, and it is made over the RAW body before
// anything reads it: a request whose signature does not hold is dropped
// without this plugin having parsed a byte a stranger sent.
func Verify(secret string, body []byte, header string) error {
	if strings.TrimSpace(header) == "" {
		return codes.Errorf(codes.Forbidden,
			"the request carries no %s, and an unsigned request is not this trigger's", SignatureHeader)
	}
	sum, found := strings.CutPrefix(strings.TrimSpace(header), SignaturePrefix)
	if !found {
		return codes.Errorf(codes.Forbidden,
			"the %s names no algorithm this trigger verifies; it is %s<hex>", SignatureHeader, SignaturePrefix)
	}
	sent, err := hex.DecodeString(sum)
	if err != nil {
		return codes.Errorf(codes.Forbidden, "the %s is not hex", SignatureHeader)
	}
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write(body)
	if !hmac.Equal(sent, mac.Sum(nil)) {
		return codes.Errorf(codes.Forbidden,
			"the %s does not hold for this body; the request is dropped and nothing was fired", SignatureHeader)
	}
	return nil
}
