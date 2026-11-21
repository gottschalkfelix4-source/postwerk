package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"sort"
	"strings"
	"time"
)

// ReportRetention is how long received DMARC and TLS reports are kept.
const ReportRetention = 400 * 24 * time.Hour

// MaxReportRecords limits the rows stored per DMARC report.
const MaxReportRecords = 5000

// DMARCPolicyPublished is the policy a DMARC report was evaluated against.
type DMARCPolicyPublished struct {
	Domain string `json:"domain"`
	ADKIM  string `json:"adkim"`
	ASPF   string `json:"aspf"`
	P      string `json:"p"`
	SP     string `json:"sp"`
	Pct    string `json:"pct"`
}

// DMARCAuthResult is one raw SPF or DKIM result of a DMARC report record.
type DMARCAuthResult struct {
	Domain   string `json:"domain"`
	Selector string `json:"selector,omitempty"`
	Scope    string `json:"scope,omitempty"`
	Result   string `json:"result"`
}

// DMARCRecord is one row (source IP) of a DMARC aggregate report. DKIM and
// SPF are the aligned results from policy_evaluated.
type DMARCRecord struct {
	SourceIP     string            `json:"sourceIp"`
	Count        int64             `json:"count"`
	Disposition  string            `json:"disposition"`
	DKIM         string            `json:"dkim"`
	SPF          string            `json:"spf"`
	HeaderFrom   string            `json:"headerFrom"`
	EnvelopeFrom string            `json:"envelopeFrom"`
	DKIMResults  []DMARCAuthResult `json:"dkimResults"`
	SPFResults   []DMARCAuthResult `json:"spfResults"`
}

// Passed reports whether DMARC passed for the record (aligned DKIM or SPF).
func (r DMARCRecord) Passed() bool { return r.DKIM == "pass" || r.SPF == "pass" }

// DMARCReport is a received DMARC aggregate report (RFC 7489 7.2).
type DMARCReport struct {
	ID         int64                `json:"id"`
	Domain     string               `json:"domain"`
	OrgName    string               `json:"orgName"`
	Email      string               `json:"email"`
	ReportID   string               `json:"reportId"`
	Begin      time.Time            `json:"begin"`
	End        time.Time            `json:"end"`
	Policy     DMARCPolicyPublished `json:"policy"`
	Messages   int64                `json:"messages"`
	Failed     int64                `json:"failed"`
	ReceivedAt time.Time            `json:"receivedAt"`
	Records    []DMARCRecord        `json:"records,omitempty"`
}

// TLSFailure is one failure detail of a TLS-RPT report (RFC 8460 4.3).
type TLSFailure struct {
	ResultType          string `json:"resultType"`
	SendingMTAIP        string `json:"sendingMtaIp"`
	ReceivingMXHostname string `json:"receivingMxHostname"`
	ReceivingMXHelo     string `json:"receivingMxHelo,omitempty"`
	ReceivingIP         string `json:"receivingIp"`
	FailedSessionCount  int64  `json:"failedSessionCount"`
	AdditionalInfo      string `json:"additionalInfo,omitempty"`
	FailureReasonCode   string `json:"failureReasonCode,omitempty"`
}

// TLSReport is one policy section of a received SMTP TLS report.
type TLSReport struct {
	ID           int64        `json:"id"`
	Domain       string       `json:"domain"`
	OrgName      string       `json:"orgName"`
	Email        string       `json:"email"`
	ReportID     string       `json:"reportId"`
	Begin        time.Time    `json:"begin"`
	End          time.Time    `json:"end"`
	PolicyType   string       `json:"policyType"`
	PolicyString []string     `json:"policyString"`
	MXHosts      []string     `json:"mxHosts"`
	SuccessCount int64        `json:"successCount"`
	FailureCount int64        `json:"failureCount"`
	Failures     []TLSFailure `json:"failures"`
	ReceivedAt   time.Time    `json:"receivedAt"`
}

// SaveDMARCReport stores a report with its records. It returns false when
// the report (same organization and report id) was stored before.
func (s *Store) SaveDMARCReport(ctx context.Context, r *DMARCReport) (bool, error) {
	if len(r.Records) > MaxReportRecords {
		r.Records = r.Records[:MaxReportRecords]
	}
	r.Messages, r.Failed = 0, 0
	for _, rec := range r.Records {
		r.Messages += rec.Count
		if !rec.Passed() {
			r.Failed += rec.Count
		}
	}
	policy, _ := json.Marshal(r.Policy)
	inserted := false
	err := s.tx(ctx, func(tx *sql.Tx) error {
		res, err := tx.ExecContext(ctx, `INSERT OR IGNORE INTO dmarc_reports(domain, org_name, email, report_id, date_begin, date_end, policy_json, messages, failed, received_at)
			VALUES (?,?,?,?,?,?,?,?,?,?)`, Normalize(r.Domain), r.OrgName, r.Email, r.ReportID, r.Begin.Unix(), r.End.Unix(), string(policy), r.Messages, r.Failed, now())
		if err != nil {
			return err
		}
		if n, _ := res.RowsAffected(); n == 0 {
			return nil
		}
		inserted = true
		r.ID, _ = res.LastInsertId()
		for _, rec := range r.Records {
			auth, _ := json.Marshal(map[string]any{"dkim": rec.DKIMResults, "spf": rec.SPFResults})
			if _, err := tx.ExecContext(ctx, `INSERT INTO dmarc_records(report_id, source_ip, count, disposition, dkim, spf, header_from, envelope_from, auth_json)
				VALUES (?,?,?,?,?,?,?,?,?)`, r.ID, rec.SourceIP, rec.Count, rec.Disposition, rec.DKIM, rec.SPF, rec.HeaderFrom, rec.EnvelopeFrom, string(auth)); err != nil {
				return err
			}
		}
		return nil
	})
	return inserted, err
}

// SaveTLSReport stores one policy section of a TLS report. It returns false
// for duplicates.
func (s *Store) SaveTLSReport(ctx context.Context, r *TLSReport) (bool, error) {
	if r.Failures == nil {
		r.Failures = []TLSFailure{}
	}
	if len(r.Failures) > MaxReportRecords {
		r.Failures = r.Failures[:MaxReportRecords]
	}
	policy, _ := json.Marshal(map[string]any{"policyString": nonNil(r.PolicyString), "mxHosts": nonNil(r.MXHosts)})
	failures, _ := json.Marshal(r.Failures)
	res, err := s.DB.ExecContext(ctx, `INSERT OR IGNORE INTO tls_reports(domain, org_name, email, report_id, date_begin, date_end, policy_type, policy_json, success_count, failure_count, failures_json, received_at)
		VALUES (?,?,?,?,?,?,?,?,?,?,?,?)`, Normalize(r.Domain), r.OrgName, r.Email, r.ReportID, r.Begin.Unix(), r.End.Unix(), r.PolicyType, string(policy),
		r.SuccessCount, r.FailureCount, string(failures), now())
	if err != nil {
		return false, err
	}
	n, _ := res.RowsAffected()
	if n > 0 {
		r.ID, _ = res.LastInsertId()
	}
	return n > 0, nil
}

// ReportFilter selects reports by domain (empty: all) and period end.
type ReportFilter struct {
	Domain string
	Since  time.Time
}

func (f ReportFilter) where(prefix string) (string, []any) {
	w := prefix + "date_end >= ?"
	args := []any{f.Since.Unix()}
	if f.Domain != "" {
		w += " AND " + prefix + "domain = ?"
		args = append(args, Normalize(f.Domain))
	}
	return w, args
}

// DMARCDomainSummary sums the DMARC reports of one domain.
type DMARCDomainSummary struct {
	Domain    string   `json:"domain"`
	Reports   int64    `json:"reports"`
	Messages  int64    `json:"messages"`
	Passed    int64    `json:"passed"`
	Failed    int64    `json:"failed"`
	Reporters []string `json:"reporters"`
}

// splitList splits the result of GROUP_CONCAT(DISTINCT x || char(31)): the
// items end with \x1f and are joined with commas.
func splitList(s string) []string {
	out := []string{}
	for _, p := range strings.Split(s, "\x1f") {
		if p = strings.TrimSpace(strings.TrimPrefix(p, ",")); p != "" {
			out = append(out, p)
		}
	}
	sort.Strings(out)
	return out
}

// DMARCSummary returns per-domain totals.
func (s *Store) DMARCSummary(ctx context.Context, f ReportFilter) ([]DMARCDomainSummary, error) {
	w, args := f.where("")
	rows, err := s.DB.QueryContext(ctx, `SELECT domain, COUNT(*), COALESCE(SUM(messages),0), COALESCE(SUM(failed),0), GROUP_CONCAT(DISTINCT org_name || char(31))
		FROM dmarc_reports WHERE `+w+` GROUP BY domain ORDER BY domain`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []DMARCDomainSummary{}
	for rows.Next() {
		var d DMARCDomainSummary
		var orgs sql.NullString
		if err := rows.Scan(&d.Domain, &d.Reports, &d.Messages, &d.Failed, &orgs); err != nil {
			return nil, err
		}
		d.Passed = d.Messages - d.Failed
		d.Reporters = splitList(orgs.String)
		out = append(out, d)
	}
	return out, rows.Err()
}

// DMARCSource aggregates all records of one sending IP for a domain.
type DMARCSource struct {
	Domain      string   `json:"domain"`
	SourceIP    string   `json:"sourceIp"`
	Count       int64    `json:"count"`
	SPFPass     int64    `json:"spfPass"`
	DKIMPass    int64    `json:"dkimPass"`
	DMARCPass   int64    `json:"dmarcPass"`
	Quarantined int64    `json:"quarantined"`
	Rejected    int64    `json:"rejected"`
	Reporters   []string `json:"reporters"`
	HeaderFrom  []string `json:"headerFrom"`
}

// DMARCSources returns the sending sources, those with failures first.
func (s *Store) DMARCSources(ctx context.Context, f ReportFilter, limit int) ([]DMARCSource, error) {
	w, args := f.where("r.")
	args = append(args, limit)
	rows, err := s.DB.QueryContext(ctx, `SELECT r.domain, c.source_ip, SUM(c.count),
		SUM(CASE WHEN c.spf = 'pass' THEN c.count ELSE 0 END),
		SUM(CASE WHEN c.dkim = 'pass' THEN c.count ELSE 0 END),
		SUM(CASE WHEN c.spf = 'pass' OR c.dkim = 'pass' THEN c.count ELSE 0 END),
		SUM(CASE WHEN c.disposition = 'quarantine' THEN c.count ELSE 0 END),
		SUM(CASE WHEN c.disposition = 'reject' THEN c.count ELSE 0 END),
		GROUP_CONCAT(DISTINCT r.org_name || char(31)), GROUP_CONCAT(DISTINCT c.header_from || char(31))
		FROM dmarc_records c JOIN dmarc_reports r ON r.id = c.report_id
		WHERE `+w+`
		GROUP BY r.domain, c.source_ip
		ORDER BY SUM(c.count) - SUM(CASE WHEN c.spf = 'pass' OR c.dkim = 'pass' THEN c.count ELSE 0 END) DESC, SUM(c.count) DESC
		LIMIT ?`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []DMARCSource{}
	for rows.Next() {
		var d DMARCSource
		var orgs, froms sql.NullString
		if err := rows.Scan(&d.Domain, &d.SourceIP, &d.Count, &d.SPFPass, &d.DKIMPass, &d.DMARCPass, &d.Quarantined, &d.Rejected, &orgs, &froms); err != nil {
			return nil, err
		}
		d.Reporters = splitList(orgs.String)
		d.HeaderFrom = splitList(froms.String)
		out = append(out, d)
	}
	return out, rows.Err()
}

// ListDMARCReports returns the newest reports (without records).
func (s *Store) ListDMARCReports(ctx context.Context, f ReportFilter, limit int) ([]DMARCReport, error) {
	w, args := f.where("")
	args = append(args, limit)
	rows, err := s.DB.QueryContext(ctx, `SELECT id, domain, org_name, email, report_id, date_begin, date_end, policy_json, messages, failed, received_at
		FROM dmarc_reports WHERE `+w+` ORDER BY date_end DESC, id DESC LIMIT ?`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []DMARCReport{}
	for rows.Next() {
		r, err := scanDMARCReport(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *r)
	}
	return out, rows.Err()
}

func scanDMARCReport(sc interface{ Scan(...any) error }) (*DMARCReport, error) {
	var r DMARCReport
	var begin, end, recv int64
	var policy string
	if err := sc.Scan(&r.ID, &r.Domain, &r.OrgName, &r.Email, &r.ReportID, &begin, &end, &policy, &r.Messages, &r.Failed, &recv); err != nil {
		return nil, err
	}
	r.Begin, r.End, r.ReceivedAt = ts(begin), ts(end), ts(recv)
	_ = json.Unmarshal([]byte(policy), &r.Policy)
	return &r, nil
}

// GetDMARCReport returns a report with its records.
func (s *Store) GetDMARCReport(ctx context.Context, id int64) (*DMARCReport, error) {
	r, err := scanDMARCReport(s.DB.QueryRowContext(ctx, `SELECT id, domain, org_name, email, report_id, date_begin, date_end, policy_json, messages, failed, received_at
		FROM dmarc_reports WHERE id = ?`, id))
	if err == sql.ErrNoRows {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	rows, err := s.DB.QueryContext(ctx, `SELECT source_ip, count, disposition, dkim, spf, header_from, envelope_from, auth_json
		FROM dmarc_records WHERE report_id = ? ORDER BY count DESC, id`, id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	r.Records = []DMARCRecord{}
	for rows.Next() {
		var rec DMARCRecord
		var auth string
		if err := rows.Scan(&rec.SourceIP, &rec.Count, &rec.Disposition, &rec.DKIM, &rec.SPF, &rec.HeaderFrom, &rec.EnvelopeFrom, &auth); err != nil {
			return nil, err
		}
		var a struct {
			DKIM []DMARCAuthResult `json:"dkim"`
			SPF  []DMARCAuthResult `json:"spf"`
		}
		_ = json.Unmarshal([]byte(auth), &a)
		rec.DKIMResults, rec.SPFResults = a.DKIM, a.SPF
		if rec.DKIMResults == nil {
			rec.DKIMResults = []DMARCAuthResult{}
		}
		if rec.SPFResults == nil {
			rec.SPFResults = []DMARCAuthResult{}
		}
		r.Records = append(r.Records, rec)
	}
	return r, rows.Err()
}

// DeleteDMARCReport removes a report and its records.
func (s *Store) DeleteDMARCReport(ctx context.Context, id int64) error {
	res, err := s.DB.ExecContext(ctx, `DELETE FROM dmarc_reports WHERE id = ?`, id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// TLSDomainSummary sums the TLS reports of one domain.
type TLSDomainSummary struct {
	Domain    string   `json:"domain"`
	Reports   int64    `json:"reports"`
	Success   int64    `json:"success"`
	Failure   int64    `json:"failure"`
	Reporters []string `json:"reporters"`
}

// TLSSummary returns per-domain totals of the TLS reports.
func (s *Store) TLSSummary(ctx context.Context, f ReportFilter) ([]TLSDomainSummary, error) {
	w, args := f.where("")
	rows, err := s.DB.QueryContext(ctx, `SELECT domain, COUNT(*), COALESCE(SUM(success_count),0), COALESCE(SUM(failure_count),0), GROUP_CONCAT(DISTINCT org_name || char(31))
		FROM tls_reports WHERE `+w+` GROUP BY domain ORDER BY domain`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []TLSDomainSummary{}
	for rows.Next() {
		var d TLSDomainSummary
		var orgs sql.NullString
		if err := rows.Scan(&d.Domain, &d.Reports, &d.Success, &d.Failure, &orgs); err != nil {
			return nil, err
		}
		d.Reporters = splitList(orgs.String)
		out = append(out, d)
	}
	return out, rows.Err()
}

// ListTLSReports returns the newest TLS reports with their failures.
func (s *Store) ListTLSReports(ctx context.Context, f ReportFilter, limit int) ([]TLSReport, error) {
	w, args := f.where("")
	args = append(args, limit)
	rows, err := s.DB.QueryContext(ctx, `SELECT id, domain, org_name, email, report_id, date_begin, date_end, policy_type, policy_json, success_count, failure_count, failures_json, received_at
		FROM tls_reports WHERE `+w+` ORDER BY date_end DESC, id DESC LIMIT ?`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []TLSReport{}
	for rows.Next() {
		var r TLSReport
		var begin, end, recv int64
		var policy, failures string
		if err := rows.Scan(&r.ID, &r.Domain, &r.OrgName, &r.Email, &r.ReportID, &begin, &end, &r.PolicyType, &policy, &r.SuccessCount, &r.FailureCount, &failures, &recv); err != nil {
			return nil, err
		}
		r.Begin, r.End, r.ReceivedAt = ts(begin), ts(end), ts(recv)
		var p struct {
			PolicyString []string `json:"policyString"`
			MXHosts      []string `json:"mxHosts"`
		}
		_ = json.Unmarshal([]byte(policy), &p)
		r.PolicyString, r.MXHosts = nonNil(p.PolicyString), nonNil(p.MXHosts)
		_ = json.Unmarshal([]byte(failures), &r.Failures)
		if r.Failures == nil {
			r.Failures = []TLSFailure{}
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// TLSFailureGroup aggregates TLS failures by domain, result type and MX.
type TLSFailureGroup struct {
	Domain              string    `json:"domain"`
	ResultType          string    `json:"resultType"`
	ReceivingMXHostname string    `json:"receivingMxHostname"`
	Sessions            int64     `json:"sessions"`
	SendingIPs          []string  `json:"sendingIps"`
	Reporters           []string  `json:"reporters"`
	LastSeen            time.Time `json:"lastSeen"`
}

// TLSFailures groups the failure details of the selected TLS reports.
func (s *Store) TLSFailures(ctx context.Context, f ReportFilter) ([]TLSFailureGroup, error) {
	reports, err := s.ListTLSReports(ctx, f, 5000)
	if err != nil {
		return nil, err
	}
	type key struct{ domain, typ, mx string }
	groups := map[key]*TLSFailureGroup{}
	ips := map[key]map[string]bool{}
	orgs := map[key]map[string]bool{}
	for _, r := range reports {
		for _, fl := range r.Failures {
			k := key{r.Domain, fl.ResultType, strings.ToLower(fl.ReceivingMXHostname)}
			g := groups[k]
			if g == nil {
				g = &TLSFailureGroup{Domain: r.Domain, ResultType: fl.ResultType, ReceivingMXHostname: fl.ReceivingMXHostname}
				groups[k] = g
				ips[k], orgs[k] = map[string]bool{}, map[string]bool{}
			}
			g.Sessions += fl.FailedSessionCount
			if fl.SendingMTAIP != "" {
				ips[k][fl.SendingMTAIP] = true
			}
			orgs[k][r.OrgName] = true
			if r.End.After(g.LastSeen) {
				g.LastSeen = r.End
			}
		}
	}
	keys := func(m map[string]bool) []string {
		out := []string{}
		for v := range m {
			out = append(out, v)
		}
		sort.Strings(out)
		return out
	}
	out := []TLSFailureGroup{}
	for k, g := range groups {
		g.SendingIPs = keys(ips[k])
		g.Reporters = keys(orgs[k])
		out = append(out, *g)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Sessions != out[j].Sessions {
			return out[i].Sessions > out[j].Sessions
		}
		return out[i].Domain+out[i].ResultType < out[j].Domain+out[j].ResultType
	})
	return out, nil
}

// DeleteTLSReport removes a TLS report.
func (s *Store) DeleteTLSReport(ctx context.Context, id int64) error {
	res, err := s.DB.ExecContext(ctx, `DELETE FROM tls_reports WHERE id = ?`, id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// purgeReports drops reports older than ReportRetention.
func (s *Store) purgeReports(ctx context.Context) {
	cutoff := time.Now().Add(-ReportRetention).Unix()
	_, _ = s.DB.ExecContext(ctx, `DELETE FROM dmarc_reports WHERE date_end < ?`, cutoff)
	_, _ = s.DB.ExecContext(ctx, `DELETE FROM tls_reports WHERE date_end < ?`, cutoff)
}
