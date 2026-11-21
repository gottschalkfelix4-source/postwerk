package reports

import (
	"archive/zip"
	"bytes"
	"compress/gzip"
	"encoding/base64"
	"errors"
	"os"
	"strings"
	"testing"
)

func fixture(t *testing.T, name string) []byte {
	t.Helper()
	b, err := os.ReadFile("testdata/" + name)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func gz(t *testing.T, data []byte) []byte {
	t.Helper()
	var b bytes.Buffer
	w := gzip.NewWriter(&b)
	w.Write(data)
	w.Close()
	return b.Bytes()
}

func zipped(t *testing.T, name string, data []byte) []byte {
	t.Helper()
	var b bytes.Buffer
	w := zip.NewWriter(&b)
	f, _ := w.Create(name)
	f.Write(data)
	w.Close()
	return b.Bytes()
}

func b64(data []byte) string {
	s := base64.StdEncoding.EncodeToString(data)
	var out strings.Builder
	for len(s) > 76 {
		out.WriteString(s[:76] + "\r\n")
		s = s[76:]
	}
	return out.String() + s + "\r\n"
}

// reportMail builds a multipart report mail with one attachment.
func reportMail(extraHeader, ct, filename string, attachment []byte) []byte {
	return []byte("From: noreply-dmarc-support@google.com\r\n" +
		"To: postmaster@example.test\r\n" +
		"Subject: Report domain: example.test Submitter: google.com Report-ID: 123\r\n" + extraHeader +
		"MIME-Version: 1.0\r\n" +
		"Content-Type: multipart/mixed; boundary=\"b1\"\r\n\r\n" +
		"--b1\r\nContent-Type: text/plain\r\n\r\nThis is an aggregate report.\r\n" +
		"--b1\r\nContent-Type: " + ct + "; name=\"" + filename + "\"\r\n" +
		"Content-Disposition: attachment; filename=\"" + filename + "\"\r\n" +
		"Content-Transfer-Encoding: base64\r\n\r\n" + b64(attachment) +
		"--b1--\r\n")
}

func checkDMARC(t *testing.T, res Result) {
	t.Helper()
	if len(res.Errors) > 0 {
		t.Fatalf("errors: %v", res.Errors)
	}
	if len(res.DMARC) != 1 {
		t.Fatalf("dmarc reports = %d", len(res.DMARC))
	}
	r := res.DMARC[0]
	if r.Domain != "example.test" || r.OrgName != "google.com" || r.ReportID != "12345678901234567890" || r.Policy.P != "quarantine" {
		t.Fatalf("metadata: %+v", r)
	}
	if r.Begin.Unix() != 1763596800 || r.End.Unix() != 1763683199 {
		t.Fatalf("range: %v - %v", r.Begin, r.End)
	}
	if len(r.Records) != 3 {
		t.Fatalf("records = %d (invalid IP must be skipped)", len(r.Records))
	}
	if rec := r.Records[0]; rec.SourceIP != "203.0.113.10" || rec.Count != 42 || !rec.Passed() || len(rec.DKIMResults) != 2 || rec.DKIMResults[1].Selector != "pw202611e" {
		t.Fatalf("record 0: %+v", rec)
	}
	if rec := r.Records[1]; rec.Passed() || rec.Disposition != "quarantine" || rec.EnvelopeFrom != "spoofer.test" || rec.SPFResults[0].Result != "softfail" {
		t.Fatalf("record 1: %+v", rec)
	}
	if rec := r.Records[2]; rec.SourceIP != "2001:db8::25" || !rec.Passed() {
		t.Fatalf("record 2: %+v", rec)
	}
}

func TestExtractDMARCZip(t *testing.T) {
	xmlDoc := fixture(t, "dmarc_aggregate.xml")
	mail := reportMail("", "application/zip", "google.com!example.test!1763596800!1763683199.zip", zipped(t, "google.com!example.test!1763596800!1763683199.xml", xmlDoc))
	checkDMARC(t, Extract(mail))
}

func TestExtractDMARCGzip(t *testing.T) {
	xmlDoc := fixture(t, "dmarc_aggregate.xml")
	mail := reportMail("", "application/gzip", "enterprise.protection.outlook.com!example.test!1763596800!1763683199.xml.gz", gz(t, xmlDoc))
	checkDMARC(t, Extract(mail))
	// Some reporters label the archive application/octet-stream.
	mail = reportMail("", "application/octet-stream", "report.xml.gz", gz(t, xmlDoc))
	checkDMARC(t, Extract(mail))
}

func TestExtractDMARCPlainXMLBody(t *testing.T) {
	xmlDoc := fixture(t, "dmarc_aggregate.xml")
	mail := []byte("From: dmarc@reporter.test\r\nTo: postmaster@example.test\r\nSubject: Report Domain: example.test\r\nContent-Type: text/xml\r\n\r\n" + string(xmlDoc))
	checkDMARC(t, Extract(mail))
}

func TestParseDMARCNamespaceAndCharset(t *testing.T) {
	doc := `<?xml version="1.0" encoding="ISO-8859-1"?>
<feedback xmlns="urn:ietf:params:xml:ns:dmarc-2.0">
 <report_metadata><org_name>M` + "\xfc" + `ller Mail</org_name><report_id>r-1</report_id>
  <date_range><begin>1</begin><end>2</end></date_range></report_metadata>
 <policy_published><domain>Example.TEST.</domain><p>reject</p></policy_published>
 <record><row><source_ip>192.0.2.1</source_ip><count>1</count>
  <policy_evaluated><disposition>reject</disposition><dkim>fail</dkim><spf>fail</spf></policy_evaluated></row>
  <identifiers><header_from>example.test</header_from></identifiers></record>
</feedback>`
	r, err := ParseDMARC([]byte(doc))
	if err != nil {
		t.Fatal(err)
	}
	if r.OrgName != "Müller Mail" || r.Domain != "example.test" || len(r.Records) != 1 || r.Records[0].Disposition != "reject" {
		t.Fatalf("parsed: %+v", r)
	}
	if _, err := ParseDMARC([]byte(`<feedback><report_metadata><org_name>x</org_name></report_metadata></feedback>`)); err == nil {
		t.Fatal("report without id/domain accepted")
	}
	if _, err := ParseDMARC([]byte(`<feedback><report_metadata><org_name>x</org_name><report_id>1</report_id><date_range><begin>a</begin><end>2</end></date_range></report_metadata><policy_published><domain>x.test</domain></policy_published></feedback>`)); err == nil {
		t.Fatal("invalid timestamp accepted")
	}
}

func TestExtractTLSRPT(t *testing.T) {
	doc := fixture(t, "tlsrpt.json")
	mail := reportMail("TLS-Report-Domain: example.test\r\nTLS-Report-Submitter: company-x.example\r\n",
		"application/tlsrpt+gzip", "company-x.example!example.test!1763510400!1763596799!001.json.gz", gz(t, doc))
	res := Extract(mail)
	if len(res.Errors) > 0 {
		t.Fatalf("errors: %v", res.Errors)
	}
	if len(res.TLS) != 2 || len(res.DMARC) != 0 {
		t.Fatalf("tls = %d, dmarc = %d", len(res.TLS), len(res.DMARC))
	}
	r := res.TLS[0]
	if r.Domain != "example.test" || r.OrgName != "Company-X" || r.PolicyType != "sts" || r.SuccessCount != 5326 || r.FailureCount != 303 {
		t.Fatalf("policy 0: %+v", r)
	}
	if len(r.Failures) != 3 || r.Failures[1].ResultType != "starttls-not-supported" || r.Failures[1].FailedSessionCount != 200 || r.Failures[2].FailureReasonCode == "" {
		t.Fatalf("failures: %+v", r.Failures)
	}
	if len(r.PolicyString) != 4 || len(r.MXHosts) != 1 {
		t.Fatalf("policy: %+v", r)
	}
	// mx-host given as a plain string is accepted as well.
	if o := res.TLS[1]; o.Domain != "other.test" || o.PolicyType != "no-policy-found" || len(o.MXHosts) != 1 || o.MXHosts[0] != "mx.other.test" {
		t.Fatalf("policy 1: %+v", o)
	}

	// Uncompressed JSON.
	mail = reportMail("", "application/tlsrpt+json", "report.json", doc)
	if res := Extract(mail); len(res.TLS) != 2 {
		t.Fatalf("plain json: %+v", res)
	}
	if _, err := ParseTLSRPT([]byte(`{"organization-name":"x","report-id":"1","policies":[]}`)); err == nil {
		t.Fatal("report without date-range accepted")
	}
}

func TestDecompressionLimits(t *testing.T) {
	bomb := bytes.Repeat([]byte("<"), MaxDecompressed+1024)
	res := Extract(reportMail("", "application/gzip", "bomb.xml.gz", gz(t, bomb)))
	if len(res.DMARC) != 0 || len(res.Errors) == 0 || !errors.Is(res.Errors[0], ErrTooLarge) {
		t.Fatalf("gzip bomb: %+v", res.Errors)
	}
	res = Extract(reportMail("", "application/zip", "bomb.zip", zipped(t, "bomb.xml", bomb)))
	if len(res.DMARC) != 0 || len(res.Errors) == 0 || !errors.Is(res.Errors[0], ErrTooLarge) {
		t.Fatalf("zip bomb: %+v", res.Errors)
	}
}

func TestNonReportMailIgnored(t *testing.T) {
	mail := []byte("From: a@b.test\r\nTo: postmaster@example.test\r\nSubject: Hallo\r\nContent-Type: multipart/mixed; boundary=x\r\n\r\n" +
		"--x\r\nContent-Type: text/plain\r\n\r\nHallo\r\n--x\r\nContent-Type: image/png; name=a.png\r\nContent-Transfer-Encoding: base64\r\n\r\niVBORw0KGgo=\r\n--x--\r\n")
	res := Extract(mail)
	if len(res.DMARC)+len(res.TLS)+len(res.Errors) != 0 {
		t.Fatalf("unexpected: %+v", res)
	}
	if !LooksLikeReport("Report Domain: example.test Submitter: google.com", "") || !LooksLikeReport("", "example.test") || LooksLikeReport("Hallo", "") {
		t.Fatal("LooksLikeReport")
	}
}
