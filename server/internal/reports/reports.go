// Package reports parses incoming DMARC aggregate reports (RFC 7489 7.2)
// and SMTP TLS reports (RFC 8460) from report mails.
package reports

import (
	"archive/zip"
	"bytes"
	"compress/gzip"
	"encoding/json"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"mime"
	"net"
	"path"
	"strconv"
	"strings"
	"time"

	"github.com/emersion/go-message"
	_ "github.com/emersion/go-message/charset"
	"golang.org/x/net/html/charset"

	"postwerk/internal/store"
)

// MaxDecompressed limits the total decompressed size of all report
// attachments of one message (protection against zip bombs).
const MaxDecompressed = 20 << 20

// maxZipEntries limits the number of files read from one zip archive.
const maxZipEntries = 10

// ErrTooLarge is returned when a report exceeds MaxDecompressed.
var ErrTooLarge = errors.New("report exceeds the size limit")

// Result holds the reports found in a message.
type Result struct {
	DMARC  []*store.DMARCReport
	TLS    []*store.TLSReport
	Errors []error
}

// budget tracks the remaining decompressed bytes.
type budget struct{ left int64 }

// read reads r completely, failing once the budget is exhausted.
func (b *budget) read(r io.Reader) ([]byte, error) {
	data, err := io.ReadAll(io.LimitReader(r, b.left+1))
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > b.left {
		b.left = 0
		return nil, ErrTooLarge
	}
	b.left -= int64(len(data))
	return data, nil
}

// Extract finds and parses all DMARC and TLS reports attached to (or
// forming the body of) a message.
func Extract(raw []byte) Result {
	var res Result
	ent, err := message.Read(bytes.NewReader(raw))
	if err != nil && ent == nil {
		res.Errors = append(res.Errors, err)
		return res
	}
	b := &budget{left: MaxDecompressed}
	_ = ent.Walk(func(_ []int, part *message.Entity, err error) error {
		if err != nil {
			res.Errors = append(res.Errors, err)
			return nil
		}
		ct, params, _ := part.Header.ContentType()
		if strings.HasPrefix(ct, "multipart/") {
			return nil
		}
		name := params["name"]
		if _, dp, err := part.Header.ContentDisposition(); err == nil && dp["filename"] != "" {
			name = dp["filename"]
		}
		kind := classify(ct, name)
		if kind == "" {
			return nil
		}
		if b.left <= 0 {
			res.Errors = append(res.Errors, ErrTooLarge)
			return nil
		}
		docs, err := decode(kind, part.Body, b)
		if err != nil {
			res.Errors = append(res.Errors, fmt.Errorf("%s: %w", name, err))
		}
		for _, doc := range docs {
			res.add(doc)
		}
		return nil
	})
	return res
}

func (r *Result) add(doc []byte) {
	trimmed := bytes.TrimSpace(bytes.TrimPrefix(doc, []byte("\xef\xbb\xbf")))
	switch {
	case bytes.HasPrefix(trimmed, []byte("{")):
		reps, err := ParseTLSRPT(trimmed)
		if err != nil {
			r.Errors = append(r.Errors, err)
			return
		}
		r.TLS = append(r.TLS, reps...)
	case bytes.HasPrefix(trimmed, []byte("<")):
		rep, err := ParseDMARC(trimmed)
		if err != nil {
			r.Errors = append(r.Errors, err)
			return
		}
		r.DMARC = append(r.DMARC, rep)
	}
}

// classify decides how a MIME part is decoded: zip, gzip, plain or "" (not
// a report).
func classify(ct, name string) string {
	ct = strings.ToLower(ct)
	ext := strings.ToLower(path.Ext(name))
	switch {
	case ct == "application/zip" || ct == "application/x-zip-compressed" || ct == "application/x-zip" || ext == ".zip":
		return "zip"
	case ct == "application/gzip" || ct == "application/x-gzip" || ct == "application/tlsrpt+gzip" || ext == ".gz" || ext == ".gzip":
		return "gzip"
	case ct == "application/tlsrpt+json" || ct == "text/xml" || ct == "application/xml" || ct == "application/json" || ext == ".xml" || ext == ".json":
		return "plain"
	case ct == "application/octet-stream" && (strings.HasSuffix(strings.ToLower(name), ".xml.gz") || strings.HasSuffix(strings.ToLower(name), ".json.gz")):
		return "gzip"
	}
	return ""
}

func decode(kind string, body io.Reader, b *budget) ([][]byte, error) {
	switch kind {
	case "plain":
		d, err := b.read(body)
		if err != nil {
			return nil, err
		}
		return [][]byte{d}, nil
	case "gzip":
		zr, err := gzip.NewReader(body)
		if err != nil {
			return nil, err
		}
		defer zr.Close()
		d, err := b.read(zr)
		if err != nil {
			return nil, err
		}
		return [][]byte{d}, nil
	case "zip":
		// The archive itself counts against the budget as well.
		archive, err := b.read(body)
		if err != nil {
			return nil, err
		}
		zr, err := zip.NewReader(bytes.NewReader(archive), int64(len(archive)))
		if err != nil {
			return nil, err
		}
		var out [][]byte
		for i, f := range zr.File {
			if i >= maxZipEntries {
				break
			}
			if f.FileInfo().IsDir() {
				continue
			}
			if f.UncompressedSize64 > uint64(b.left) {
				return out, ErrTooLarge
			}
			rc, err := f.Open()
			if err != nil {
				return out, err
			}
			d, err := b.read(rc)
			rc.Close()
			if err != nil {
				return out, err
			}
			out = append(out, d)
		}
		return out, nil
	}
	return nil, nil
}

// ---- DMARC aggregate reports ----

type xmlFeedback struct {
	XMLName  xml.Name `xml:"feedback"`
	Metadata struct {
		OrgName   string `xml:"org_name"`
		Email     string `xml:"email"`
		ReportID  string `xml:"report_id"`
		DateRange struct {
			Begin string `xml:"begin"`
			End   string `xml:"end"`
		} `xml:"date_range"`
	} `xml:"report_metadata"`
	Policy struct {
		Domain string `xml:"domain"`
		ADKIM  string `xml:"adkim"`
		ASPF   string `xml:"aspf"`
		P      string `xml:"p"`
		SP     string `xml:"sp"`
		Pct    string `xml:"pct"`
	} `xml:"policy_published"`
	Records []struct {
		Row struct {
			SourceIP string `xml:"source_ip"`
			Count    string `xml:"count"`
			Policy   struct {
				Disposition string `xml:"disposition"`
				DKIM        string `xml:"dkim"`
				SPF         string `xml:"spf"`
			} `xml:"policy_evaluated"`
		} `xml:"row"`
		Identifiers struct {
			HeaderFrom   string `xml:"header_from"`
			EnvelopeFrom string `xml:"envelope_from"`
		} `xml:"identifiers"`
		Auth struct {
			DKIM []struct {
				Domain   string `xml:"domain"`
				Selector string `xml:"selector"`
				Result   string `xml:"result"`
			} `xml:"dkim"`
			SPF []struct {
				Domain string `xml:"domain"`
				Scope  string `xml:"scope"`
				Result string `xml:"result"`
			} `xml:"spf"`
		} `xml:"auth_results"`
	} `xml:"record"`
}

func clean(s string, max int) string {
	s = strings.TrimSpace(s)
	s = strings.Map(func(r rune) rune {
		if r < 0x20 || r == 0x7f {
			return -1
		}
		return r
	}, s)
	if len(s) > max {
		s = strings.ToValidUTF8(s[:max], "")
	}
	return s
}

func lower(s string) string { return strings.ToLower(clean(s, 64)) }

func unixTime(s string) (time.Time, error) {
	n, err := strconv.ParseInt(strings.TrimSpace(s), 10, 64)
	if err != nil {
		return time.Time{}, fmt.Errorf("invalid timestamp %q", s)
	}
	return time.Unix(n, 0).UTC(), nil
}

// ParseDMARC parses a DMARC aggregate report document (XML).
func ParseDMARC(data []byte) (*store.DMARCReport, error) {
	dec := xml.NewDecoder(bytes.NewReader(data))
	dec.CharsetReader = charset.NewReaderLabel
	var fb xmlFeedback
	if err := dec.Decode(&fb); err != nil {
		return nil, fmt.Errorf("dmarc report: %w", err)
	}
	r := &store.DMARCReport{
		Domain:   strings.TrimSuffix(lower(fb.Policy.Domain), "."),
		OrgName:  clean(fb.Metadata.OrgName, 200),
		Email:    clean(fb.Metadata.Email, 200),
		ReportID: clean(fb.Metadata.ReportID, 200),
		Policy: store.DMARCPolicyPublished{
			Domain: lower(fb.Policy.Domain), ADKIM: lower(fb.Policy.ADKIM), ASPF: lower(fb.Policy.ASPF),
			P: lower(fb.Policy.P), SP: lower(fb.Policy.SP), Pct: clean(fb.Policy.Pct, 8),
		},
		Records: []store.DMARCRecord{},
	}
	if r.Domain == "" || r.OrgName == "" || r.ReportID == "" {
		return nil, errors.New("dmarc report: missing domain, org_name or report_id")
	}
	var err error
	if r.Begin, err = unixTime(fb.Metadata.DateRange.Begin); err != nil {
		return nil, fmt.Errorf("dmarc report: %w", err)
	}
	if r.End, err = unixTime(fb.Metadata.DateRange.End); err != nil {
		return nil, fmt.Errorf("dmarc report: %w", err)
	}
	for _, x := range fb.Records {
		ip := strings.TrimSpace(x.Row.SourceIP)
		if net.ParseIP(ip) == nil {
			continue
		}
		n, err := strconv.ParseInt(strings.TrimSpace(x.Row.Count), 10, 64)
		if err != nil || n < 0 {
			continue
		}
		rec := store.DMARCRecord{
			SourceIP: ip, Count: n,
			Disposition: lower(x.Row.Policy.Disposition), DKIM: lower(x.Row.Policy.DKIM), SPF: lower(x.Row.Policy.SPF),
			HeaderFrom: lower(x.Identifiers.HeaderFrom), EnvelopeFrom: lower(x.Identifiers.EnvelopeFrom),
			DKIMResults: []store.DMARCAuthResult{}, SPFResults: []store.DMARCAuthResult{},
		}
		for _, d := range x.Auth.DKIM {
			rec.DKIMResults = append(rec.DKIMResults, store.DMARCAuthResult{Domain: lower(d.Domain), Selector: clean(d.Selector, 64), Result: lower(d.Result)})
		}
		for _, s := range x.Auth.SPF {
			rec.SPFResults = append(rec.SPFResults, store.DMARCAuthResult{Domain: lower(s.Domain), Scope: lower(s.Scope), Result: lower(s.Result)})
		}
		r.Records = append(r.Records, rec)
		if len(r.Records) >= store.MaxReportRecords {
			break
		}
	}
	return r, nil
}

// ---- TLS reports ----

// stringList accepts a JSON string or an array of strings.
type stringList []string

func (l *stringList) UnmarshalJSON(b []byte) error {
	var one string
	if err := json.Unmarshal(b, &one); err == nil {
		*l = []string{one}
		return nil
	}
	var many []string
	if err := json.Unmarshal(b, &many); err != nil {
		return err
	}
	*l = many
	return nil
}

type jsonTLSReport struct {
	OrgName   string `json:"organization-name"`
	DateRange struct {
		Start time.Time `json:"start-datetime"`
		End   time.Time `json:"end-datetime"`
	} `json:"date-range"`
	ContactInfo string `json:"contact-info"`
	ReportID    string `json:"report-id"`
	Policies    []struct {
		Policy struct {
			Type   string     `json:"policy-type"`
			String stringList `json:"policy-string"`
			Domain string     `json:"policy-domain"`
			MXHost stringList `json:"mx-host"`
		} `json:"policy"`
		Summary struct {
			Success int64 `json:"total-successful-session-count"`
			Failure int64 `json:"total-failure-session-count"`
		} `json:"summary"`
		Failures []struct {
			ResultType          string `json:"result-type"`
			SendingMTAIP        string `json:"sending-mta-ip"`
			ReceivingMXHostname string `json:"receiving-mx-hostname"`
			ReceivingMXHelo     string `json:"receiving-mx-helo"`
			ReceivingIP         string `json:"receiving-ip"`
			FailedSessionCount  int64  `json:"failed-session-count"`
			AdditionalInfo      string `json:"additional-information"`
			FailureReasonCode   string `json:"failure-reason-code"`
		} `json:"failure-details"`
	} `json:"policies"`
}

func cleanList(l []string, maxItems int) []string {
	out := []string{}
	for _, s := range l {
		if len(out) >= maxItems {
			break
		}
		if s = clean(s, 300); s != "" {
			out = append(out, s)
		}
	}
	return out
}

// ParseTLSRPT parses an SMTP TLS report (JSON) into one entry per policy.
func ParseTLSRPT(data []byte) ([]*store.TLSReport, error) {
	var j jsonTLSReport
	if err := json.Unmarshal(data, &j); err != nil {
		return nil, fmt.Errorf("tls report: %w", err)
	}
	org, id := clean(j.OrgName, 200), clean(j.ReportID, 200)
	if org == "" || id == "" {
		return nil, errors.New("tls report: missing organization-name or report-id")
	}
	if j.DateRange.Start.IsZero() || j.DateRange.End.IsZero() {
		return nil, errors.New("tls report: missing date-range")
	}
	var out []*store.TLSReport
	for _, p := range j.Policies {
		domain := strings.TrimSuffix(lower(p.Policy.Domain), ".")
		if domain == "" {
			continue
		}
		r := &store.TLSReport{
			Domain: domain, OrgName: org, Email: clean(j.ContactInfo, 200), ReportID: id,
			Begin: j.DateRange.Start.UTC(), End: j.DateRange.End.UTC(),
			PolicyType:   lower(p.Policy.Type),
			PolicyString: cleanList(p.Policy.String, 50),
			MXHosts:      cleanList(p.Policy.MXHost, 50),
			SuccessCount: max(p.Summary.Success, 0), FailureCount: max(p.Summary.Failure, 0),
			Failures: []store.TLSFailure{},
		}
		for _, f := range p.Failures {
			if len(r.Failures) >= store.MaxReportRecords {
				break
			}
			r.Failures = append(r.Failures, store.TLSFailure{
				ResultType: lower(f.ResultType), SendingMTAIP: clean(f.SendingMTAIP, 64),
				ReceivingMXHostname: clean(f.ReceivingMXHostname, 255), ReceivingMXHelo: clean(f.ReceivingMXHelo, 255),
				ReceivingIP: clean(f.ReceivingIP, 64), FailedSessionCount: max(f.FailedSessionCount, 0),
				AdditionalInfo: clean(f.AdditionalInfo, 500), FailureReasonCode: clean(f.FailureReasonCode, 200),
			})
		}
		out = append(out, r)
	}
	if len(out) == 0 {
		return nil, errors.New("tls report: no policies")
	}
	return out, nil
}

// LooksLikeReport reports whether a message header suggests a DMARC or TLS
// report (used in addition to the recipient address).
func LooksLikeReport(subject, tlsReportDomain string) bool {
	if tlsReportDomain != "" {
		return true
	}
	s := strings.ToLower(subject)
	if dec, err := new(mime.WordDecoder).DecodeHeader(subject); err == nil {
		s = strings.ToLower(dec)
	}
	return strings.Contains(s, "report domain:") || strings.HasPrefix(s, "report domain") || strings.Contains(s, "dmarc aggregate report")
}
