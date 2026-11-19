package auth

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"strings"
	"time"
)

// signer keeps what a cookie holds readable but unchangeable: the value is JSON and a MAC over it, each in base64.
type signer struct {
	key []byte
}

// sign writes value, which runs out at the given time, into a cookie value.
func (s signer) sign(purpose string, value any, expires time.Time) (string, error) {
	payload, err := json.Marshal(struct {
		Value   any   `json:"v"`
		Expires int64 `json:"e"`
	}{value, expires.Unix()})
	if err != nil {
		return "", err
	}
	encoded := base64.RawURLEncoding.EncodeToString(payload)
	return encoded + "." + base64.RawURLEncoding.EncodeToString(s.mac(purpose, encoded)), nil
}

// open reads a cookie value back into value, unless it was changed, signed for another purpose or has run out.
func (s signer) open(purpose, cookie string, value any, now time.Time) error {
	encoded, mac, found := strings.Cut(cookie, ".")
	if !found {
		return errors.New("not a signed value")
	}
	given, err := base64.RawURLEncoding.DecodeString(mac)
	if err != nil || !hmac.Equal(given, s.mac(purpose, encoded)) {
		return errors.New("the signature does not match")
	}
	payload, err := base64.RawURLEncoding.DecodeString(encoded)
	if err != nil {
		return err
	}

	var wrapped struct {
		Value   json.RawMessage `json:"v"`
		Expires int64           `json:"e"`
	}
	if err := json.Unmarshal(payload, &wrapped); err != nil {
		return err
	}
	if now.Unix() >= wrapped.Expires {
		return errors.New("run out")
	}
	return json.Unmarshal(wrapped.Value, value)
}

// mac binds the value to its purpose, so that a cookie of one kind cannot be passed off as another.
func (s signer) mac(purpose, encoded string) []byte {
	h := hmac.New(sha256.New, s.key)
	h.Write([]byte(purpose))
	h.Write([]byte{0})
	h.Write([]byte(encoded))
	return h.Sum(nil)
}
