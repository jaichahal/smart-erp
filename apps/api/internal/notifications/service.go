package notifications

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/riverqueue/river"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"

	"github.com/jaichahal/smart-erp/apps/api/internal/kit/audit"
	"github.com/jaichahal/smart-erp/apps/api/internal/kit/config"
	"github.com/jaichahal/smart-erp/apps/api/internal/kit/httpx"
	"github.com/jaichahal/smart-erp/apps/api/internal/kit/outbox"
	"github.com/jaichahal/smart-erp/apps/api/internal/kit/rls"
)

// Directory is the identity read this module needs. Identity implements it.
type Directory interface {
	Users(ctx context.Context, companyID string) ([]User, error)
}

// MasterApproval is how a sensitive master change asks approvals to open a
// request inside the same transaction. Approvals implements it.
type MasterApproval interface {
	Request(ctx context.Context, tx pgx.Tx, kind string, payload any) error
}

// LiveDocument is the document the shade refetches before an action (R13.4, E10).
type LiveDocument struct {
	ID           string   `json:"id"`
	State        string   `json:"state"`
	StateVersion int64    `json:"state_version"`
	FraudHints   []string `json:"fraud_hints"`
	DecidedBy    string   `json:"decided_by,omitempty"`
}

// Approvals fetches the live document and commits a decision at a state version.
type Approvals interface {
	Fetch(ctx context.Context, id string) (LiveDocument, error)
	Decide(ctx context.Context, id string, stateVersion int64, action, reason string) error
}

// Service is the notifications module.
type Service struct {
	Pool           *pgxpool.Pool
	River          *river.Client[pgx.Tx]
	Config         *config.Config
	Log            *slog.Logger
	Hub            *Hub
	Push           PushSender
	Mail           Mailer
	Directory      Directory
	MasterApproval MasterApproval
	Approvals      Approvals
	Now            func() time.Time
}

// New builds a service from kit Deps. Options override test doubles.
func New(deps httpx.Deps, opts ...Option) (*Service, error) {
	if err := RequirePushCredentials(deps.Config); err != nil {
		return nil, err
	}
	log := deps.Log
	if log == nil {
		log = slog.Default()
	}
	s := &Service{
		Pool:   deps.Pool,
		River:  deps.River,
		Config: deps.Config,
		Log:    log,
		Hub:    NewHub(),
		Now:    func() time.Time { return time.Now().UTC() },
	}
	for _, opt := range opts {
		opt(s)
	}
	if s.Push == nil {
		s.Push = routeSender{cfg: deps.Config, client: nil}
	}
	return s, nil
}

// Option configures a Service.
type Option func(*Service)

// WithPush replaces the push sender.
func WithPush(p PushSender) Option { return func(s *Service) { s.Push = p } }

// WithMailer sets the email fallback.
func WithMailer(m Mailer) Option { return func(s *Service) { s.Mail = m } }

// WithDirectory sets the identity directory.
func WithDirectory(d Directory) Option { return func(s *Service) { s.Directory = d } }

// WithApprovals sets the live-document reader used by approve-from-shade.
func WithApprovals(a Approvals) Option { return func(s *Service) { s.Approvals = a } }

// WithMasterApproval sets the hook for alert-rule changes.
func WithMasterApproval(m MasterApproval) Option {
	return func(s *Service) { s.MasterApproval = m }
}

// WithClock overrides the clock.
func WithClock(now func() time.Time) Option { return func(s *Service) { s.Now = now } }

// WithHub replaces the socket hub.
func WithHub(h *Hub) Option { return func(s *Service) { s.Hub = h } }

// FanoutArgs is the River job that carries one validated event (ADR-04).
type FanoutArgs struct {
	Event    Event    `json:"event"`
	Roles    []string `json:"roles,omitempty"`
	Explicit []string `json:"explicit,omitempty"`
}

// Kind is the River job name.
func (FanoutArgs) Kind() string { return "notifications.fanout" }

// InsertOpts places the job on the notifications queue and dedupes identical args.
func (FanoutArgs) InsertOpts() river.InsertOpts {
	return river.InsertOpts{
		Queue:       outbox.QueueNotifications,
		MaxAttempts: 8,
		UniqueOpts:  river.UniqueOpts{ByArgs: true},
	}
}

// Enqueue validates the event and inserts the outbox job in the caller's
// transaction. An invalid amount never reaches the queue (E4).
func Enqueue(ctx context.Context, client *river.Client[pgx.Tx], tx pgx.Tx, args FanoutArgs) error {
	ev, err := reparse(args.Event)
	if err != nil {
		return err
	}
	args.Event = ev
	if _, err := outbox.InsertTx(ctx, client, tx, args, nil); err != nil {
		return err
	}
	return nil
}

func reparse(ev Event) (Event, error) {
	raw, err := json.Marshal(ev)
	if err != nil {
		return Event{}, err
	}
	parsed, err := ParseEvent(raw)
	if err != nil {
		return Event{}, err
	}
	if err := parsed.Validate(); err != nil {
		return Event{}, err
	}
	return parsed, nil
}

// CommitSubmission writes the business row and the outbox job in one
// transaction so a crash between them leaves neither (E1). Alert dispatch is
// the job, not this transaction (E11).
func (s *Service) CommitSubmission(ctx context.Context, p rls.Principal, docType, docID string, ev Event, roles, explicit []string) error {
	return rls.Tx(ctx, s.Pool, p, func(tx pgx.Tx) error {
		if _, err := insertSubmission(ctx, tx, p.CompanyID.String(), docType, docID, "submitted"); err != nil {
			return err
		}
		ev.CompanyID = p.CompanyID.String()
		return Enqueue(ctx, s.River, tx, FanoutArgs{Event: ev, Roles: roles, Explicit: explicit})
	})
}

// Submit applies alert rules. Blocking mode returns before the business write.
// Advisory mode commits the write and the outbox job (E12, E11).
func (s *Service) Submit(ctx context.Context, p rls.Principal, docType, docID, amount string, ev Event, roles, explicit []string) error {
	return rls.Tx(ctx, s.Pool, p, func(tx pgx.Tx) error {
		rules, err := listRules(ctx, tx, p.CompanyID.String(), docType)
		if err != nil {
			return err
		}
		for _, rule := range rules {
			if rule.Mode == "blocking" && ruleMatches(rule, amount) {
				// Committed on its own so the refusal survives the rollback of this submission (R2.7).
				if _, err := audit.EmitCommitted(ctx, s.Pool, p, audit.Event{
					Type: "exception.raised", ReferenceType: docType, ReferenceID: docID, Reason: "blocking_alert",
				}); err != nil {
					return err
				}
				return ErrBlockingAlert
			}
		}
		if _, err := insertSubmission(ctx, tx, p.CompanyID.String(), docType, docID, "submitted"); err != nil {
			return err
		}
		advisory := false
		for _, rule := range rules {
			if rule.Mode == "advisory" && ruleMatches(rule, amount) {
				advisory = true
				ev.Severity = rule.Severity
				if len(rule.RecipientRoles) > 0 {
					roles = rule.RecipientRoles
				}
			}
		}
		if !advisory && ev.EventID == "" {
			return nil
		}
		if ev.EventID == "" {
			id, err := NewEventID(s.Now())
			if err != nil {
				return err
			}
			ev.EventID = id
		}
		ev.CompanyID = p.CompanyID.String()
		if ev.Subject.DocType == "" {
			ev.Subject.DocType = docType
		}
		if ev.Subject.DocID == "" {
			ev.Subject.DocID = docID
		}
		if ev.OccurredAt.IsZero() {
			ev.OccurredAt = s.Now()
		}
		if ev.Context == nil {
			ev.Context = map[string]any{}
		}
		if ev.DeepLink == "" {
			ev.DeepLink = "smarterp://document/" + docType + "/" + docID
		}
		return Enqueue(ctx, s.River, tx, FanoutArgs{Event: ev, Roles: roles, Explicit: explicit})
	})
}

// FanoutWorker consumes notifications.fanout jobs.
type FanoutWorker struct {
	river.WorkerDefaults[FanoutArgs]
	Svc *Service
}

// Work delivers one event. A duplicate event_id is a success (E2).
func (w *FanoutWorker) Work(ctx context.Context, job *river.Job[FanoutArgs]) error {
	ctx, span := otel.Tracer("notifications").Start(ctx, "notifications.fanout")
	defer span.End()
	span.SetAttributes(attribute.String("event_id", job.Args.Event.EventID))
	if err := job.Args.Event.Validate(); err != nil {
		return river.JobCancel(err)
	}
	return w.Svc.Deliver(ctx, job.Args, job.Attempt)
}

// NextRetry backs off by attempt so a failed delivery is retried later (E8).
func (w *FanoutWorker) NextRetry(job *river.Job[FanoutArgs]) time.Time {
	shift := job.Attempt
	if shift < 1 {
		shift = 1
	}
	if shift > 8 {
		shift = 8
	}
	return time.Now().Add(time.Second * time.Duration(int(1)<<shift))
}

// PruneArgs is the scheduled token prune.
type PruneArgs struct{}

// Kind is the River job name.
func (PruneArgs) Kind() string { return "notifications.prune_tokens" }

// PruneWorker deletes stale device tokens.
type PruneWorker struct {
	river.WorkerDefaults[PruneArgs]
	Svc *Service
}

// Work prunes tokens unseen for 60 days.
func (w *PruneWorker) Work(ctx context.Context, _ *river.Job[PruneArgs]) error {
	return rls.Tx(ctx, w.Svc.Pool, rls.System, func(tx pgx.Tx) error {
		_, err := PruneTokens(ctx, tx, w.Svc.Now())
		return err
	})
}

// RegisterWorkers adds this module's River workers. cmd/worker calls it; that
// file is owned by Track A, so the call is not in this PR.
func RegisterWorkers(workers *river.Workers, svc *Service) {
	river.AddWorker(workers, &FanoutWorker{Svc: svc})
	river.AddWorker(workers, &PruneWorker{Svc: svc})
}

// Deliver fans one event out. Push failure records a Failed row, sends email
// as its own row, and returns an error so River retries with backoff (E8).
// The business write that enqueued the job has already committed (E11).
func (s *Service) Deliver(ctx context.Context, args FanoutArgs, attempt int) error {
	if attempt < 1 {
		attempt = 1
	}
	ev := args.Event
	p := workerPrincipal(ev.CompanyID)
	var retry error
	err := rls.Tx(ctx, s.Pool, p, func(tx pgx.Tx) error {
		done, err := alreadyConsumed(ctx, tx, ev.EventID)
		if err != nil {
			return err
		}
		if done {
			return nil
		}
		users, err := s.users(ctx, ev.CompanyID)
		if err != nil {
			return err
		}
		recipients := DeriveRecipients(ev.Actor.ID, args.Roles, args.Explicit, users)
		var failed *retryablePush
		for _, u := range recipients {
			if err := s.deliverOne(ctx, tx, p, ev, u, attempt); err != nil {
				var rp *retryablePush
				if !errors.As(err, &rp) {
					return err
				}
				failed = rp
			}
		}
		if failed != nil {
			retry = failed
			return nil
		}
		_, err = markConsumed(ctx, tx, ev.EventID, ev.CompanyID)
		return err
	})
	if err != nil {
		return err
	}
	return retry
}

func (s *Service) users(ctx context.Context, company string) ([]User, error) {
	if s.Directory == nil {
		return nil, nil
	}
	return s.Directory.Users(ctx, company)
}

func (s *Service) deliverOne(ctx context.Context, tx pgx.Tx, p rls.Principal, ev Event, u User, attempt int) error {
	group := Group(ev.AllowedActions, ev.Severity)
	if err := insertInbox(ctx, tx, ev.CompanyID, u.ID, ev, group); err != nil {
		return err
	}
	quiet, hasQuiet, err := loadQuiet(ctx, tx, ev.CompanyID, u.ID)
	if err != nil {
		return err
	}
	suppressed := hasQuiet && inQuiet(quiet, s.Now()) && ev.Severity != "CRITICAL"
	pushOn, err := channelEnabled(ctx, tx, ev.CompanyID, u.ID, ev.Type, "push", "*")
	if err != nil {
		return err
	}
	if isFYI(ev.AllowedActions, ev.Severity) {
		digestOn, err := channelEnabled(ctx, tx, ev.CompanyID, u.ID, ev.Type, "digest", "*")
		if err != nil {
			return err
		}
		if digestOn && !suppressed {
			return queueDigest(ctx, tx, ev.CompanyID, u.ID, ev)
		}
		return nil
	}
	if suppressed || !pushOn {
		return nil
	}
	sent, err := deliverySent(ctx, tx, ev.CompanyID, ev.EventID, u.ID, "push")
	if err != nil {
		return err
	}
	if sent {
		return nil
	}
	wsOn, err := channelEnabled(ctx, tx, ev.CompanyID, u.ID, ev.Type, "websocket", "*")
	if err != nil {
		return err
	}
	if wsOn {
		if err := s.publish(u.ID, ev); err != nil {
			return err
		}
	}
	tokens, err := listTokens(ctx, tx, ev.CompanyID, u.ID)
	if err != nil {
		return err
	}
	data, err := ev.PushData()
	if err != nil {
		return err
	}
	var pushErr error
	for _, tok := range tokens {
		deviceOn, err := channelEnabled(ctx, tx, ev.CompanyID, u.ID, ev.Type, "push", tok.DeviceID)
		if err != nil {
			return err
		}
		if !deviceOn {
			continue
		}
		err = s.Push.Send(ctx, tok.Platform, tok.Token, data)
		if err == nil {
			if err := recordDelivery(ctx, tx, ev.CompanyID, delivery{
				EventID: ev.EventID, Channel: "push", UserID: u.ID, Status: "sent", Attempt: attempt,
			}, s.Now()); err != nil {
				return err
			}
			continue
		}
		var unreg *ErrTokenUnregistered
		if errors.As(err, &unreg) {
			if err := deleteTokenValue(ctx, tx, tok.Token); err != nil {
				return err
			}
			if err := recordDelivery(ctx, tx, ev.CompanyID, delivery{
				EventID: ev.EventID, Channel: "push", UserID: u.ID, Status: "failed", Attempt: attempt, ErrorCode: "unregistered",
			}, s.Now()); err != nil {
				return err
			}
			continue
		}
		pushErr = err
		if err := recordDelivery(ctx, tx, ev.CompanyID, delivery{
			EventID: ev.EventID, Channel: "push", UserID: u.ID, Status: "failed", Attempt: attempt, ErrorCode: "push_failed",
		}, s.Now()); err != nil {
			return err
		}
		if err := s.emailFallback(ctx, tx, p, ev, u, attempt); err != nil {
			return err
		}
	}
	if pushErr != nil {
		return &retryablePush{err: pushErr}
	}
	return nil
}

func (s *Service) emailFallback(ctx context.Context, tx pgx.Tx, _ rls.Principal, ev Event, u User, attempt int) error {
	code := ""
	status := "sent"
	if u.Email == "" || s.Mail == nil {
		status = "failed"
		code = "email_unavailable"
	} else if err := s.Mail.Send(ctx, u.Email, ev.Type, ev.EventID); err != nil {
		status = "failed"
		code = "email_failed"
	}
	return recordDelivery(ctx, tx, ev.CompanyID, delivery{
		EventID: ev.EventID, Channel: "email", UserID: u.ID, Status: status, Attempt: attempt, ErrorCode: code,
	}, s.Now())
}

func (s *Service) publish(userID string, ev Event) error {
	body, err := json.Marshal(socketMessage{
		EventID: ev.EventID,
		Type:    ev.Type,
		Payload: ev,
		At:      s.Now(),
	})
	if err != nil {
		return err
	}
	s.Hub.Publish(userID, body)
	return nil
}

type socketMessage struct {
	EventID string    `json:"event_id"`
	Type    string    `json:"type"`
	Payload any       `json:"payload"`
	At      time.Time `json:"at"`
}

// FlushDigest sends one email per user for queued FYI events (E6) and records
// one delivery row for the batch.
func (s *Service) FlushDigest(ctx context.Context, company string) error {
	p := workerPrincipal(company)
	return rls.Tx(ctx, s.Pool, p, func(tx pgx.Tx) error {
		batches, err := unflushedDigest(ctx, tx, company)
		if err != nil {
			return err
		}
		users, err := s.users(ctx, company)
		if err != nil {
			return err
		}
		byID := map[string]User{}
		for _, u := range users {
			byID[u.ID] = u
		}
		for _, b := range batches {
			u := byID[b.UserID]
			raw, err := json.Marshal(b.EventIDs)
			if err != nil {
				return err
			}
			status, code := "sent", ""
			if s.Mail == nil || u.Email == "" {
				status, code = "failed", "email_unavailable"
			} else if err := s.Mail.Send(ctx, u.Email, "digest", string(raw)); err != nil {
				status, code = "failed", "email_failed"
			}
			if err := recordDelivery(ctx, tx, company, delivery{
				EventID: b.EventIDs[0], Channel: "digest", UserID: b.UserID, Status: status, Attempt: 1, ErrorCode: code,
			}, s.Now()); err != nil {
				return err
			}
			if err := markDigestFlushed(ctx, tx, company, b.UserID, s.Now()); err != nil {
				return err
			}
		}
		return nil
	})
}

type retryablePush struct{ err error }

func (e *retryablePush) Error() string { return e.err.Error() }
func (e *retryablePush) Unwrap() error { return e.err }

func workerPrincipal(company string) rls.Principal {
	p := rls.Principal{UserID: "system", Roles: []string{"system"}}
	if id, err := uuid.Parse(company); err == nil {
		p.CompanyID = id
	}
	return p
}
