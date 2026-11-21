package mailauth

import (
	"crypto/sha256"
	"encoding/hex"
	"strconv"
	"strings"
)

// MTA-STS modes of our own policy (RFC 8461 §3.2).
const (
	MTASTSNone    = "none"
	MTASTSTesting = "testing"
	MTASTSEnforce = "enforce"
)

// MTA-STS max_age bounds in seconds: the default is one week, RFC 8461
// allows at most one year; less than a day defeats the purpose.
const (
	DefaultMTASTSMaxAge = 604800
	MinMTASTSMaxAge     = 86400
	MaxMTASTSMaxAge     = 31557600
)

// ValidMTASTSMode reports whether mode is one of none, testing, enforce.
func ValidMTASTSMode(mode string) bool {
	switch mode {
	case MTASTSNone, MTASTSTesting, MTASTSEnforce:
		return true
	}
	return false
}

// MTASTSPolicy renders the policy file served at
// https://mta-sts.<domain>/.well-known/mta-sts.txt. Unknown modes fall back
// to testing, a max_age of 0 to the default.
func MTASTSPolicy(mode string, mx []string, maxAge int) string {
	if !ValidMTASTSMode(mode) {
		mode = MTASTSTesting
	}
	if maxAge <= 0 {
		maxAge = DefaultMTASTSMaxAge
	}
	var b strings.Builder
	b.WriteString("version: STSv1\r\nmode: " + mode + "\r\n")
	for _, h := range mx {
		if h = strings.TrimSuffix(strings.TrimSpace(strings.ToLower(h)), "."); h != "" {
			b.WriteString("mx: " + h + "\r\n")
		}
	}
	b.WriteString("max_age: " + strconv.Itoa(maxAge) + "\r\n")
	return b.String()
}

// MTASTSPolicyID derives the id of the _mta-sts TXT record from the policy
// text, so the id changes exactly when the policy does (RFC 8461 §3.1:
// 1-32 alphanumeric characters).
func MTASTSPolicyID(policy string) string {
	sum := sha256.Sum256([]byte(policy))
	return hex.EncodeToString(sum[:])[:20]
}
