// Package httpx holds small HTTP client helpers shared by the integrations.
package httpx

import (
	"errors"
	"net/http"
)

// SameHostRedirect is an http.Client CheckRedirect policy for requests that
// carry credentials (API keys, tokens, Basic auth) or private content. Go
// forwards custom headers such as x-api-key, and re-sends the body on 307
// and 308, to whatever host a redirect names; this policy follows a redirect
// only to the same host, and never from https down to http.
func SameHostRedirect(req *http.Request, via []*http.Request) error {
	if len(via) >= 5 {
		return errors.New("too many redirects")
	}
	first := via[0].URL
	if req.URL.Host != first.Host || (first.Scheme == "https" && req.URL.Scheme != "https") {
		return http.ErrUseLastResponse
	}
	return nil
}
