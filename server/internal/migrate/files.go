package migrate

import (
	"archive/zip"
	"bufio"
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"path"
	"strconv"
	"strings"
	"time"

	"postwerk/internal/delivery"
	"postwerk/internal/message"
	"postwerk/internal/store"
)

type ImportResult struct {
	Imported int      `json:"imported"`
	Skipped  int      `json:"skipped"`
	Failed   int      `json:"failed"`
	Errors   []string `json:"errors"`
}

func (r *ImportResult) fail(format string, args ...any) {
	r.Failed++
	if len(r.Errors) < 50 {
		r.Errors = append(r.Errors, fmt.Sprintf(format, args...))
	}
}

// FileImporter imports .mbox, .eml and .zip files into one account.
type FileImporter struct {
	Delivery *delivery.Service
	Account  *store.Account
	// MailboxID is the fixed target; 0 means inbox (and zip subfolders become folders).
	MailboxID int64
	MaxSize   int

	keys map[int64]map[string]bool
	res  ImportResult
}

// ImportFile detects the format by name and content.
func (fi *FileImporter) ImportFile(ctx context.Context, filePath, name string) (*ImportResult, error) {
	fi.keys = map[int64]map[string]bool{}
	fi.res = ImportResult{Errors: []string{}}
	f, err := os.Open(filePath)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		return nil, err
	}
	head := make([]byte, 5)
	n, _ := io.ReadFull(f, head)
	head = head[:n]
	if _, err := f.Seek(0, io.SeekStart); err != nil {
		return nil, err
	}
	lower := strings.ToLower(name)
	switch {
	case strings.HasSuffix(lower, ".zip") || bytes.HasPrefix(head, []byte("PK\x03\x04")):
		zr, err := zip.NewReader(f, st.Size())
		if err != nil {
			return nil, fmt.Errorf("ZIP-Datei unlesbar: %w", err)
		}
		if err := fi.importZip(ctx, zr); err != nil {
			return nil, err
		}
	case strings.HasSuffix(lower, ".eml"):
		raw, err := io.ReadAll(io.LimitReader(f, int64(fi.MaxSize)+1))
		if err != nil {
			return nil, err
		}
		mb, err := fi.mailbox(ctx, "")
		if err != nil {
			return nil, err
		}
		fi.store(ctx, mb, raw, time.Time{}, false, false, name)
	default: // mbox (also files without extension, e.g. Thunderbird's "Inbox")
		mb, err := fi.mailbox(ctx, "")
		if err != nil {
			return nil, err
		}
		if err := fi.importMbox(ctx, f, mb, name); err != nil {
			return nil, err
		}
	}
	return &fi.res, nil
}

func (fi *FileImporter) mailbox(ctx context.Context, folder string) (int64, error) {
	if fi.MailboxID != 0 {
		return fi.MailboxID, nil
	}
	if folder == "" || folder == "." {
		return fi.Delivery.Store.MailboxByRole(ctx, fi.Account.ID, "inbox")
	}
	target, _ := mapFolder(folder, "/", nil)
	switch target {
	case "inbox", "sent", "drafts", "junk", "trash", "archive":
		return fi.Delivery.Store.MailboxByRole(ctx, fi.Account.ID, target)
	}
	return fi.Delivery.Store.EnsureMailbox(ctx, fi.Account.ID, target)
}

func (fi *FileImporter) store(ctx context.Context, mb int64, raw []byte, date time.Time, seen, flagged bool, label string) {
	if len(bytes.TrimSpace(raw)) == 0 {
		return
	}
	if len(raw) > fi.MaxSize {
		fi.res.fail("%s: Nachricht zu groß (%d MB)", label, len(raw)>>20)
		return
	}
	raw = toCRLF(raw)
	keys, ok := fi.keys[mb]
	if !ok {
		k, err := fi.Delivery.Store.MessageKeys(ctx, mb)
		if err != nil {
			fi.res.fail("%s: %v", label, err)
			return
		}
		keys = k
		fi.keys[mb] = keys
	}
	idKey, hashKey := delivery.ImportKey(raw)
	if (idKey != "" && keys[idKey]) || keys[hashKey] {
		fi.res.Skipped++
		return
	}
	if !seen && !flagged {
		seen, flagged = statusFlags(raw)
	}
	if _, err := fi.Delivery.ImportMessage(ctx, fi.Account, mb, raw, delivery.ImportOptions{ReceivedAt: date, Seen: seen, Flagged: flagged, SkipAI: true}); err != nil {
		fi.res.fail("%s: %v", label, err)
		return
	}
	keys[hashKey] = true
	if idKey != "" {
		keys[idKey] = true
	}
	fi.res.Imported++
}

func (fi *FileImporter) importZip(ctx context.Context, zr *zip.Reader) error {
	for _, zf := range zr.File {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if zf.FileInfo().IsDir() || strings.HasPrefix(path.Base(zf.Name), ".") || strings.Contains(zf.Name, "__MACOSX") {
			continue
		}
		lower := strings.ToLower(zf.Name)
		dir := path.Dir(strings.ReplaceAll(zf.Name, "\\", "/"))
		switch {
		case strings.HasSuffix(lower, ".eml"):
			mb, err := fi.mailbox(ctx, dir)
			if err != nil {
				return err
			}
			rc, err := zf.Open()
			if err != nil {
				fi.res.fail("%s: %v", zf.Name, err)
				continue
			}
			raw, err := io.ReadAll(io.LimitReader(rc, int64(fi.MaxSize)+1))
			rc.Close()
			if err != nil {
				fi.res.fail("%s: %v", zf.Name, err)
				continue
			}
			fi.store(ctx, mb, raw, time.Time{}, false, false, zf.Name)
		case strings.HasSuffix(lower, ".mbox") || strings.HasSuffix(lower, "/mbox"):
			folder := strings.TrimSuffix(strings.TrimSuffix(zf.Name, ".mbox"), "/mbox")
			// Apple Mail exports "Name.mbox/mbox"
			folder = strings.TrimSuffix(folder, ".mbox")
			mb, err := fi.mailbox(ctx, folder)
			if err != nil {
				return err
			}
			rc, err := zf.Open()
			if err != nil {
				fi.res.fail("%s: %v", zf.Name, err)
				continue
			}
			err = fi.importMbox(ctx, rc, mb, zf.Name)
			rc.Close()
			if err != nil {
				return err
			}
		}
	}
	return nil
}

// importMbox splits an mbox (mboxo/mboxrd) stream into messages.
func (fi *FileImporter) importMbox(ctx context.Context, r io.Reader, mb int64, label string) error {
	br := bufio.NewReaderSize(r, 64<<10)
	var cur bytes.Buffer
	var date time.Time
	started := false
	prevBlank := true
	n := 0
	flush := func() {
		if started {
			n++
			fi.store(ctx, mb, cur.Bytes(), date, false, false, fmt.Sprintf("%s #%d", label, n))
		}
		cur.Reset()
	}
	for {
		line, err := br.ReadBytes('\n')
		if len(line) > 0 {
			if bytes.HasPrefix(line, []byte("From ")) && prevBlank {
				flush()
				if ctx.Err() != nil {
					return ctx.Err()
				}
				started = true
				date = fromLineDate(string(bytes.TrimRight(line, "\r\n")))
				prevBlank = false
				continue
			}
			if !started {
				// Not an mbox: treat whole input as a single message.
				started = true
			}
			trimmed := bytes.TrimRight(line, "\r\n")
			prevBlank = len(trimmed) == 0
			// mboxrd/mboxo unescaping: ">From " -> "From ", ">>From " -> ">From "
			if bytes.HasPrefix(bytes.TrimLeft(trimmed, ">"), []byte("From ")) && bytes.HasPrefix(trimmed, []byte(">")) {
				line = line[1:]
			}
			if cur.Len() < fi.MaxSize+1 {
				cur.Write(line)
			}
		}
		if err == io.EOF {
			break
		}
		if err != nil {
			return err
		}
	}
	// The blank line before the next "From " belongs to the separator, not the message.
	flush()
	return nil
}

func fromLineDate(line string) time.Time {
	// "From sender@example.com Mon Jan  2 15:04:05 2006"
	f := strings.Fields(line)
	if len(f) >= 7 {
		s := strings.Join(f[len(f)-5:], " ")
		if t, err := time.Parse("Mon Jan 2 15:04:05 2006", s); err == nil {
			return t
		}
	}
	return time.Time{}
}

func toCRLF(raw []byte) []byte {
	if !bytes.Contains(raw, []byte("\n")) || bytes.Count(raw, []byte("\r\n")) == bytes.Count(raw, []byte("\n")) {
		return raw
	}
	raw = bytes.ReplaceAll(raw, []byte("\r\n"), []byte("\n"))
	return bytes.ReplaceAll(raw, []byte("\n"), []byte("\r\n"))
}

// statusFlags reads read/flagged state from mbox headers (Status, X-Status, X-Mozilla-Status).
func statusFlags(raw []byte) (seen, flagged bool) {
	if s := message.HeaderValue(raw, "Status"); strings.Contains(s, "R") {
		seen = true
	}
	if s := message.HeaderValue(raw, "X-Status"); strings.Contains(s, "F") {
		flagged = true
	}
	if s := message.HeaderValue(raw, "X-Mozilla-Status"); s != "" {
		if v, err := strconv.ParseUint(strings.TrimSpace(s), 16, 32); err == nil {
			seen = seen || v&0x0001 != 0
			flagged = flagged || v&0x0004 != 0
		}
	}
	return
}
