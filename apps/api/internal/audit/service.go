package audit

import (
	"log/slog"
	"os"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/riverqueue/river"

	"github.com/jaichahal/smart-erp/apps/api/internal/kit/config"
	"github.com/jaichahal/smart-erp/apps/api/internal/kit/httpx"
)

// Refusal codes a restore returns. force overrides only SITE_MISMATCH (I4).
const (
	RefuseFileChecksum       = "FILE_CHECKSUM"
	RefuseAggregateChecksum  = "AGGREGATE_CHECKSUM"
	RefuseManifestMissing    = "MANIFEST_MISSING"
	RefuseManifestIncomplete = "MANIFEST_INCOMPLETE"
	RefuseCountMismatch      = "COUNT_MISMATCH"
	RefuseLedgerMismatch     = "LEDGER_MISMATCH"
	RefuseSiteMismatch       = "SITE_MISMATCH"
	RefuseAnchorMismatch     = "ANCHOR_MISMATCH"
	RefuseChainBreak         = "CHAIN_BREAK"
	RefuseApprovalRequired   = "APPROVAL_REQUIRED"
)

// Service runs anchoring, verification, backup, and restore for one process.
type Service struct {
	Pool              *pgxpool.Pool
	River             *river.Client[pgx.Tx]
	OnPrem            ObjectStore
	Offsite           ObjectStore
	KMS               *LocalKMS
	Site              string
	Now               func() time.Time
	RetainFor         time.Duration
	ObjectRetain      time.Duration
	Mail              Mailer
	StakeholderEmails []string
	AuditorEmail      string
	Lang              string
	Log               *slog.Logger
	pendingWAL        []walBytes
	SkipOnPrem        bool
	SkipOffsite       bool
}

func (s *Service) lang() string {
	if s == nil || s.Lang == "" {
		return "en"
	}
	return s.Lang
}

func (s *Service) now() time.Time {
	if s != nil && s.Now != nil {
		return s.Now().UTC()
	}
	return time.Now().UTC()
}

func (s *Service) log() *slog.Logger {
	if s != nil && s.Log != nil {
		return s.Log
	}
	return slog.Default()
}

// NewFromDeps wires stores from the process config. A missing signing seed
// leaves KMS nil; anchor calls then fail with the variable name.
func NewFromDeps(deps httpx.Deps) *Service {
	cfg := deps.Config
	if cfg == nil {
		cfg = &config.Config{}
	}
	svc := &Service{
		Pool:              deps.Pool,
		River:             deps.River,
		OnPrem:            NewS3Client(cfg.S3Endpoint, os.Getenv("ERP_S3_REGION"), cfg.S3AccessKey, cfg.S3SecretKey, cfg.S3BucketAnchors),
		Offsite:           NewS3Client(cfg.OffsiteS3Endpoint, os.Getenv("ERP_S3_REGION"), cfg.OffsiteS3AccessKey, cfg.OffsiteS3SecretKey, cfg.OffsiteS3Bucket),
		Site:              cfg.Env,
		Mail:              smtpMailer{addr: cfg.SMTPAddr},
		StakeholderEmails: splitList(os.Getenv("ERP_STAKEHOLDER_EMAILS")),
		AuditorEmail:      os.Getenv("ERP_AUDITOR_EMAIL"),
		Log:               deps.Log,
	}
	if seed := strings.TrimSpace(os.Getenv("ERP_ANCHOR_SIGNING_SEED")); seed != "" {
		keyID := strings.TrimSpace(os.Getenv("ERP_ANCHOR_KEY_ID"))
		if keyID == "" {
			keyID = "local-sim"
		}
		kms, err := NewLocalKMS(keyID, SeedFromString(seed))
		if err != nil {
			svc.log().Error("anchor key", "err", err)
		} else {
			svc.KMS = kms
		}
	}
	return svc
}

// MissingAnchorEnv names signing settings the anchor worker needs beyond kit/config.
func MissingAnchorEnv() []string {
	var miss []string
	for _, k := range []string{"ERP_ANCHOR_KEY_ID", "ERP_ANCHOR_SIGNING_SEED"} {
		if strings.TrimSpace(os.Getenv(k)) == "" {
			miss = append(miss, k)
		}
	}
	return miss
}

func splitList(v string) []string {
	if strings.TrimSpace(v) == "" {
		return nil
	}
	var out []string
	for _, p := range strings.Split(v, ",") {
		p = strings.TrimSpace(p)
		if p != "" {
			out = append(out, p)
		}
	}
	return out
}
