package backup

import (
	"io"
	"net/http"
	"strings"
	"testing"
)

func TestStatusErrDoesNotEchoBody(t *testing.T) {
	res := &http.Response{StatusCode: 403, Body: io.NopCloser(strings.NewReader("internal secret token=abc"))}
	err := statusErr("Hochladen", res)
	if strings.Contains(err.Error(), "secret") || !strings.Contains(err.Error(), "403 Forbidden") {
		t.Fatalf("err = %v", err)
	}
}
