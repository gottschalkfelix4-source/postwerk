package jmap

import (
	"context"
	"errors"
	"io"
	"mime"
	"net/http"
	"strconv"
	"strings"
	"time"

	"postwerk/internal/message"
	"postwerk/internal/store"
)

// blobData returns the content and media type of a blob the account may
// read: an upload, a stored message, or a part of one of those.
func (s *Server) blobData(ctx context.Context, accountID int64, blobID string) ([]byte, string, error) {
	hash, path, ok := parseBlobID(blobID)
	if !ok || !s.Store.BlobAccessible(ctx, accountID, hash) {
		return nil, "", errBlobNotFound
	}
	raw, err := s.Store.Blobs.Get(hash)
	if err != nil {
		return nil, "", errBlobNotFound
	}
	if len(path) == 0 {
		return raw, "", nil
	}
	p := extractPart(raw, path, 0)
	if p == nil {
		return nil, "", errBlobNotFound
	}
	return p.body, p.mediaType, nil
}

func (s *Server) handleUpload(w http.ResponseWriter, r *http.Request) {
	acct, ok := s.authenticate(w, r)
	if !ok {
		return
	}
	accountID := r.PathValue("accountId")
	if accountID != fmtID(prefixAccount, acct.ID) {
		s.problem(w, http.StatusNotFound, "", "unknown account")
		return
	}
	release, ok := s.acquire("upload", acct.ID, maxConcurrentUpload)
	if !ok {
		s.problem(w, http.StatusTooManyRequests, "urn:ietf:params:jmap:error:limit", "too many concurrent uploads", "limit", "maxConcurrentUpload")
		return
	}
	defer release()
	_ = http.NewResponseController(w).SetReadDeadline(time.Now().Add(15 * time.Minute))
	limit := s.maxSize()
	data, err := io.ReadAll(http.MaxBytesReader(w, r.Body, limit))
	if err != nil {
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			s.problem(w, http.StatusRequestEntityTooLarge, "urn:ietf:params:jmap:error:limit", "upload too large", "limit", "maxSizeUpload")
			return
		}
		s.problem(w, http.StatusBadRequest, "", "could not read upload")
		return
	}
	if s.Store.PendingUploadBytes(r.Context(), acct.ID)+int64(len(data)) > store.MaxPendingUploadBytes ||
		s.Store.CheckQuota(r.Context(), acct.ID, int64(len(data))) != nil {
		s.problem(w, http.StatusRequestEntityTooLarge, "urn:ietf:params:jmap:error:limit", "storage quota or upload limit reached", "limit", "maxSizeUpload")
		return
	}
	typ := "application/octet-stream"
	if mt, params, err := mime.ParseMediaType(r.Header.Get("Content-Type")); err == nil && strings.Count(mt, "/") == 1 {
		typ = mime.FormatMediaType(strings.ToLower(mt), params)
		if typ == "" {
			typ = strings.ToLower(mt)
		}
	}
	hash, err := s.Store.Blobs.Put(data)
	if err == nil {
		err = s.Store.AddUpload(r.Context(), acct.ID, hash, typ, int64(len(data)))
	}
	if err != nil {
		s.Log.Error("jmap upload failed", "component", "jmap", "user", acct.Email, "err", err)
		s.problem(w, http.StatusInternalServerError, "", "upload could not be stored")
		return
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(http.StatusCreated)
	writeJSON(w, map[string]any{"accountId": accountID, "blobId": prefixBlob + hash, "type": typ, "size": len(data)})
}

func (s *Server) handleDownload(w http.ResponseWriter, r *http.Request) {
	acct, ok := s.authenticate(w, r)
	if !ok {
		return
	}
	if r.PathValue("accountId") != fmtID(prefixAccount, acct.ID) {
		s.problem(w, http.StatusNotFound, "", "unknown account")
		return
	}
	data, partType, err := s.blobData(r.Context(), acct.ID, r.PathValue("blobId"))
	if err != nil {
		s.problem(w, http.StatusNotFound, "", "blob not found")
		return
	}
	typ := "application/octet-stream"
	if mt, _, err := mime.ParseMediaType(r.URL.Query().Get("type")); err == nil && strings.Count(mt, "/") == 1 {
		typ = strings.ToLower(mt)
	} else if partType != "" {
		typ = partType
	}
	name := message.SanitizeFilename(r.PathValue("name"))
	if name == "" {
		name = "download"
	}
	h := w.Header()
	h.Set("Content-Type", typ)
	h.Set("Content-Length", strconv.Itoa(len(data)))
	// Always a download, never rendered in the web interface's origin.
	h.Set("Content-Disposition", mime.FormatMediaType("attachment", map[string]string{"filename": name}))
	h.Set("X-Content-Type-Options", "nosniff")
	h.Set("Content-Security-Policy", "default-src 'none'; sandbox")
	h.Set("Cache-Control", "private, max-age=31536000, immutable")
	if r.Method == http.MethodHead {
		return
	}
	_, _ = w.Write(data)
}
