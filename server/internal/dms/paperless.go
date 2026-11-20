// Package dms forwards attachments to a document management system
// (currently Paperless-ngx).
package dms

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"strings"
	"time"

	"postwerk/internal/store"
)

var ErrDisabled = errors.New("paperless disabled")

// Forward uploads one document via POST /api/documents/post_document/.
func Forward(ctx context.Context, cfg store.PaperlessSettings, filename, contentType string, data []byte, title string) error {
	if !cfg.Enabled || cfg.URL == "" || cfg.Token == "" {
		return ErrDisabled
	}
	var body bytes.Buffer
	w := multipart.NewWriter(&body)
	part, err := w.CreatePart(map[string][]string{
		"Content-Disposition": {fmt.Sprintf(`form-data; name="document"; filename="%s"`, strings.ReplaceAll(filename, `"`, ""))},
		"Content-Type":        {contentType},
	})
	if err != nil {
		return err
	}
	part.Write(data)
	if title != "" {
		w.WriteField("title", title)
	}
	w.Close()

	ctx, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(cfg.URL, "/")+"/api/documents/post_document/", &body)
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", w.FormDataContentType())
	req.Header.Set("Authorization", "Token "+cfg.Token)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 500))
		return fmt.Errorf("paperless: %s %s", resp.Status, string(b))
	}
	return nil
}

// Supported reports whether Paperless can ingest this content type.
func Supported(contentType, filename string) bool {
	ct := strings.ToLower(contentType)
	fn := strings.ToLower(filename)
	return ct == "application/pdf" || strings.HasSuffix(fn, ".pdf") ||
		strings.HasPrefix(ct, "image/png") || strings.HasPrefix(ct, "image/jpeg") || strings.HasPrefix(ct, "image/tiff") ||
		strings.HasSuffix(fn, ".docx") || strings.HasSuffix(fn, ".odt")
}
