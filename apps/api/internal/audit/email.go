package audit

import (
	"context"
	"fmt"
	"net/smtp"
	"strings"
	"sync"
)

// Mailer sends the daily anchor email. Production uses SMTP; tests capture it.
type Mailer interface {
	Send(ctx context.Context, to []string, subject, body string) error
}

type smtpMailer struct{ addr string }

// Send delivers one message over SMTP.
func (m smtpMailer) Send(ctx context.Context, to []string, subject, body string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if m.addr == "" {
		return fmt.Errorf("%s", Text("en", "config.missing", "ERP_SMTP_ADDR"))
	}
	msg := []byte("Subject: " + subject + "\r\n\r\n" + body + "\r\n")
	return smtp.SendMail(m.addr, nil, "anchor@smart-erp.local", to, msg)
}

type captureMail struct {
	mu   sync.Mutex
	msgs []capturedMail
}

type capturedMail struct {
	To      []string
	Subject string
	Body    string
}

// Send records the message for a test assertion.
func (m *captureMail) Send(ctx context.Context, to []string, subject, body string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.msgs = append(m.msgs, capturedMail{To: append([]string(nil), to...), Subject: subject, Body: body})
	return nil
}

// SendDailyAnchorEmail emails the latest anchor per company to Stakeholders and the auditor (R3.6).
func (s *Service) SendDailyAnchorEmail(ctx context.Context) error {
	if s.Mail == nil {
		return fmt.Errorf("%s", Text(s.lang(), "config.missing", "ERP_SMTP_ADDR"))
	}
	to := append([]string(nil), s.StakeholderEmails...)
	if s.AuditorEmail != "" {
		to = append(to, s.AuditorEmail)
	}
	if len(to) == 0 {
		return fmt.Errorf("%s", Text(s.lang(), "config.missing", "ERP_STAKEHOLDER_EMAILS"))
	}
	rows, err := s.Pool.Query(ctx, `SELECT DISTINCT ON (company_id) company_id::text, chain_seq, head_hash, anchored_at::text
		FROM erp.anchors ORDER BY company_id, chain_seq DESC`)
	if err != nil {
		return err
	}
	defer rows.Close()
	var lines []string
	for rows.Next() {
		var company, head, at string
		var seq int64
		if err := rows.Scan(&company, &seq, &head, &at); err != nil {
			return err
		}
		lines = append(lines, Text(s.lang(), "email.anchor.body", company, seq, head, at))
	}
	if err := rows.Err(); err != nil {
		return err
	}
	if len(lines) == 0 {
		return nil
	}
	return s.Mail.Send(ctx, to, Text(s.lang(), "email.anchor.subject"), strings.Join(lines, "\n"))
}
