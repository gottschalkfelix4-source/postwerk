package migrate

import (
	"archive/zip"
	"bufio"
	"bytes"
	"context"
	"errors"
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
	// MaxLine bounds a single mbox line; longer lines make the message fail.
	// 0 means DefaultMaxLine.
	MaxLine int
	// MaxTotal caps the bytes decompressed from a ZIP archive. 0 means
	// min(ZipExpansion × archive size, MaxTotalCap).
	MaxTotal int64
	// MaxEntries caps the number of entries in a ZIP archive. 0 means DefaultMaxEntries.
	MaxEntries int

	keys      map[int64]map[string]bool
	res       ImportResult
	remaining int64 // decompression budget left for the current ZIP
}

const (
	DefaultMaxLine    = 4 << 20
	DefaultMaxEntries = 100_000
	MaxTotalCap       = 20 << 30
	// ZipExpansion is the allowed ratio of decompressed to compressed bytes;
	// plain-text mail rarely compresses better than 1:8 with deflate.
	ZipExpansion = 10
)

// errBudget is returned once a ZIP archive decompresses to more than allowed.
var errBudget = errors.New("entpackte Datenmenge überschreitet das Limit")

// budgetReader charges every byte read against the importer's ZIP budget.
type budgetReader struct {
	r  io.Reader
	fi *FileImporter
}

func (b *budgetReader) Read(p []byte) (int, error) {
	if b.fi.remaining <= 0 {
		return 0, errBudget
	}
	if int64(len(p)) > b.fi.remaining {
		p = p[:b.fi.remaining]
	}
	n, err := b.r.Read(p)
	b.fi.remaining -= int64(n)
	return n, err
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
		maxEntries := fi.MaxEntries
		if maxEntries <= 0 {
			maxEntries = DefaultMaxEntries
		}
		if len(zr.File) > maxEntries {
			return nil, fmt.Errorf("ZIP-Datei enthält zu viele Einträge (%d, erlaubt sind %d)", len(zr.File), maxEntries)
		}
		fi.remaining = fi.MaxTotal
		if fi.remaining <= 0 {
			fi.remaining = min(ZipExpansion*st.Size(), MaxTotalCap)
		}
		if err := fi.importZip(ctx, zr); err != nil {
			if !errors.Is(err, errBudget) {
				return nil, err
			}
			fi.res.fail("Import abgebrochen: %v", err)
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
			raw, err := io.ReadAll(io.LimitReader(&budgetReader{rc, fi}, int64(fi.MaxSize)+1))
			rc.Close()
			if errors.Is(err, errBudget) {
				return err
			}
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
			err = fi.importMbox(ctx, &budgetReader{rc, fi}, mb, zf.Name)
			rc.Close()
			if err != nil {
				return err
			}
		}
	}
	return nil
}

// importMbox splits an mbox (mboxo/mboxrd) stream into messages. Memory use is
// bounded: overlong lines are discarded and make their message fail.
func (fi *FileImporter) importMbox(ctx context.Context, r io.Reader, mb int64, label string) error {
	maxLine := fi.MaxLine
	if maxLine <= 0 {
		maxLine = DefaultMaxLine
	}
	br := bufio.NewReaderSize(r, 64<<10)
	var cur bytes.Buffer
	var date time.Time
	started := false
	longLine := false
	prevBlank := true
	n := 0
	flush := func() {
		if started {
			n++
			lbl := fmt.Sprintf("%s #%d", label, n)
			if longLine {
				fi.res.fail("%s: Zeile länger als %d KB, Nachricht übersprungen", lbl, maxLine>>10)
			} else {
				fi.store(ctx, mb, cur.Bytes(), date, false, false, lbl)
			}
		}
		cur.Reset()
		longLine = false
	}
	var buf []byte
	for {
		line, tooLong, err := readLine(br, buf[:0], maxLine)
		buf = line
		switch {
		case tooLong:
			started = true
			longLine = true
			prevBlank = false
		case len(line) > 0 && prevBlank && bytes.HasPrefix(line, []byte("From ")):
			flush()
			if ctx.Err() != nil {
				return ctx.Err()
			}
			started = true
			date = fromLineDate(string(bytes.TrimRight(line, "\r\n")))
			prevBlank = false
		case len(line) > 0:
			// Without a leading "From " line the whole input is a single message.
			started = true
			trimmed := bytes.TrimRight(line, "\r\n")
			prevBlank = len(trimmed) == 0
			// mboxrd/mboxo unescaping: ">From " -> "From ", ">>From " -> ">From "
			if bytes.HasPrefix(bytes.TrimLeft(trimmed, ">"), []byte("From ")) && bytes.HasPrefix(trimmed, []byte(">")) {
				line = line[1:]
			}
			if !longLine && cur.Len() < fi.MaxSize+1 {
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

// readLine reads one line (including its newline) into dst. If the line is
// longer than max bytes, the rest of it is consumed and discarded and tooLong
// is reported; the returned slice then holds no usable data.
func readLine(br *bufio.Reader, dst []byte, max int) (line []byte, tooLong bool, err error) {
	for {
		frag, e := br.ReadSlice('\n')
		if !tooLong {
			if len(dst)+len(frag) > max {
				tooLong = true
				dst = dst[:0]
			} else {
				dst = append(dst, frag...)
			}
		}
		if e == bufio.ErrBufferFull {
			continue
		}
		return dst, tooLong, e
	}
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
