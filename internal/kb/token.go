package kb

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"strconv"
	"time"
)

// FeedbackSigner issues the token that a public article page carries in its feedback form.
// The token is the same for everyone within a day, so a cached page stays byte-identical and
// keeps its ETag. It proves the post came from a page this server rendered, for that article,
// in the last two days; abuse is bounded by the per-IP rate limit, not by the token.
type FeedbackSigner struct{ keys [][]byte }

// NewFeedbackSigner signs with the first key and accepts tokens made with any of them, so a
// key rotation does not reject forms of pages served just before it.
func NewFeedbackSigner(keys ...[]byte) FeedbackSigner { return FeedbackSigner{keys: keys} }

const feedbackWindowDays = 2

func sign(key []byte, articleID string, day int64) string {
	mac := hmac.New(sha256.New, key)
	mac.Write([]byte("kb-feedback\x00" + articleID + "\x00" + strconv.FormatInt(day, 10)))
	return base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
}

func (s FeedbackSigner) Token(articleID string, now time.Time) string {
	return sign(s.keys[0], articleID, now.UTC().Unix()/86400)
}

func (s FeedbackSigner) Valid(articleID, token string, now time.Time) bool {
	today := now.UTC().Unix() / 86400
	valid := false
	for _, key := range s.keys {
		for d := range int64(feedbackWindowDays) {
			valid = hmac.Equal([]byte(sign(key, articleID, today-d)), []byte(token)) || valid
		}
	}
	return valid
}
