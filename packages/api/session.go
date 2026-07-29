package api

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strconv"
	"strings"
	"time"
)

const (
	sessionCookieName = "connect_it_admin"
	sessionTTL        = 24 * time.Hour
)

// signSession builds a cookie value of the form
// "admin|<expiry unix>|<hex(hmac-sha256)>".
func signSession(secret []byte, expires time.Time) string {
	payload := fmt.Sprintf("admin|%d", expires.Unix())
	mac := hmac.New(sha256.New, secret)
	mac.Write([]byte(payload))
	return payload + "|" + hex.EncodeToString(mac.Sum(nil))
}

func verifySession(secret []byte, value string) bool {
	parts := strings.Split(value, "|")
	if len(parts) != 3 || parts[0] != "admin" {
		return false
	}
	exp, err := strconv.ParseInt(parts[1], 10, 64)
	if err != nil || time.Now().Unix() > exp {
		return false
	}
	mac := hmac.New(sha256.New, secret)
	mac.Write([]byte(parts[0] + "|" + parts[1]))
	want := hex.EncodeToString(mac.Sum(nil))
	return hmac.Equal([]byte(want), []byte(parts[2]))
}
