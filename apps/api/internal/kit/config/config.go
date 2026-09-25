// Package config loads environment configuration and fails fast, by name, when a
// required value is missing (R17.1). Zero and false are valid values; only an
// absent or blank variable is missing.
package config

import (
	"errors"
	"fmt"
	"os"
	"sort"
	"strings"
)

// Config is everything a Smart ERP binary reads from the environment.
type Config struct {
	Env                 string // dev | prod
	HTTPAddr            string
	DatabaseURL         string // erp_app
	MigratorDatabaseURL string // erp_migrator
	AdminDatabaseURL    string // superuser; optional in prod runtime, required for migrate and tests
	ValkeyURL           string
	S3Endpoint          string
	S3AccessKey         string
	S3SecretKey         string
	S3BucketFiles       string
	S3BucketBackups     string
	S3BucketAnchors     string
	OffsiteS3Endpoint   string
	OffsiteS3AccessKey  string
	OffsiteS3SecretKey  string
	OffsiteS3Bucket     string
	ZitadelURL          string
	ZitadelPATFile      string
	ChromiumURL         string
	SMTPAddr            string
	PushMode            string // sink | live
	PushSinkURL         string
	OTLPEndpoint        string
	LogLevel            string
	Version             string
	Commit              string
}

// Requirement names the variables a given binary needs.
type Requirement struct {
	Required []string
	Optional []string
}

// Profiles per binary. Keep in sync with deploy/compose/.env.dev.example.
var (
	APIProfile = Requirement{
		Required: []string{"ERP_ENV", "ERP_HTTP_ADDR", "ERP_DATABASE_URL", "ERP_VALKEY_URL", "ERP_S3_ENDPOINT", "ERP_S3_ACCESS_KEY", "ERP_S3_SECRET_KEY", "ERP_S3_BUCKET_FILES", "ERP_S3_BUCKET_BACKUPS", "ERP_S3_BUCKET_ANCHORS", "ERP_OFFSITE_S3_ENDPOINT", "ERP_OFFSITE_S3_ACCESS_KEY", "ERP_OFFSITE_S3_SECRET_KEY", "ERP_OFFSITE_S3_BUCKET", "ERP_ZITADEL_URL", "ERP_ZITADEL_PAT_FILE", "ERP_PUSH_MODE"},
		Optional: []string{"ERP_CHROMIUM_URL", "ERP_SMTP_ADDR", "ERP_PUSH_SINK_URL", "ERP_OTLP_ENDPOINT", "ERP_LOG_LEVEL", "ERP_ADMIN_DATABASE_URL", "ERP_MIGRATOR_DATABASE_URL"},
	}
	WorkerProfile  = APIProfile
	MigrateProfile = Requirement{
		Required: []string{"ERP_MIGRATOR_DATABASE_URL", "ERP_ADMIN_DATABASE_URL"},
		Optional: []string{"ERP_LOG_LEVEL"},
	}
	TestProfile = Requirement{
		Required: []string{"ERP_ADMIN_DATABASE_URL", "ERP_MIGRATOR_DATABASE_URL", "ERP_DATABASE_URL"},
	}
)

// Load reads the environment for the profile. It returns a single error naming
// every missing variable so the operator fixes them in one pass.
func Load(p Requirement) (*Config, error) {
	var missing []string
	for _, k := range p.Required {
		if strings.TrimSpace(os.Getenv(k)) == "" {
			missing = append(missing, k)
		}
	}
	if len(missing) > 0 {
		sort.Strings(missing)
		return nil, fmt.Errorf("missing required environment: %s", strings.Join(missing, ", "))
	}
	c := &Config{
		Env:                 get("ERP_ENV", "dev"),
		HTTPAddr:            get("ERP_HTTP_ADDR", ":8080"),
		DatabaseURL:         os.Getenv("ERP_DATABASE_URL"),
		MigratorDatabaseURL: os.Getenv("ERP_MIGRATOR_DATABASE_URL"),
		AdminDatabaseURL:    os.Getenv("ERP_ADMIN_DATABASE_URL"),
		ValkeyURL:           os.Getenv("ERP_VALKEY_URL"),
		S3Endpoint:          os.Getenv("ERP_S3_ENDPOINT"),
		S3AccessKey:         os.Getenv("ERP_S3_ACCESS_KEY"),
		S3SecretKey:         os.Getenv("ERP_S3_SECRET_KEY"),
		S3BucketFiles:       os.Getenv("ERP_S3_BUCKET_FILES"),
		S3BucketBackups:     os.Getenv("ERP_S3_BUCKET_BACKUPS"),
		S3BucketAnchors:     os.Getenv("ERP_S3_BUCKET_ANCHORS"),
		OffsiteS3Endpoint:   os.Getenv("ERP_OFFSITE_S3_ENDPOINT"),
		OffsiteS3AccessKey:  os.Getenv("ERP_OFFSITE_S3_ACCESS_KEY"),
		OffsiteS3SecretKey:  os.Getenv("ERP_OFFSITE_S3_SECRET_KEY"),
		OffsiteS3Bucket:     os.Getenv("ERP_OFFSITE_S3_BUCKET"),
		ZitadelURL:          os.Getenv("ERP_ZITADEL_URL"),
		ZitadelPATFile:      os.Getenv("ERP_ZITADEL_PAT_FILE"),
		ChromiumURL:         os.Getenv("ERP_CHROMIUM_URL"),
		SMTPAddr:            os.Getenv("ERP_SMTP_ADDR"),
		PushMode:            get("ERP_PUSH_MODE", "sink"),
		PushSinkURL:         os.Getenv("ERP_PUSH_SINK_URL"),
		OTLPEndpoint:        os.Getenv("ERP_OTLP_ENDPOINT"),
		LogLevel:            get("ERP_LOG_LEVEL", "info"),
		Version:             get("ERP_VERSION", "dev"),
		Commit:              get("ERP_COMMIT", "unknown"),
	}
	if c.Env != "dev" && c.Env != "prod" {
		return nil, errors.New("ERP_ENV must be dev or prod")
	}
	if c.PushMode != "sink" && c.PushMode != "live" {
		return nil, errors.New("ERP_PUSH_MODE must be sink or live")
	}
	if c.Env == "prod" && c.PushMode == "sink" {
		return nil, errors.New("ERP_PUSH_MODE=sink is not allowed when ERP_ENV=prod")
	}
	return c, nil
}

func get(k, def string) string {
	if v := strings.TrimSpace(os.Getenv(k)); v != "" {
		return v
	}
	return def
}
