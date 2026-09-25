package identity

import (
	"context"
	"net/url"
	"os"
	"strings"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/structpb"

	"github.com/jaichahal/smart-erp/apps/api/internal/identity/zitadelproto"
)

// ZitadelBroker calls Zitadel Session API v2 over gRPC. The server stays a
// separate unmodified process (ADR-15); this client is generated from the
// Apache-2.0 proto subset in zitadelproto.
type ZitadelBroker struct {
	client zitadelproto.SessionServiceClient
}

// DialZitadel dials the Session API. rawURL is http(s)://host:port. pat is the
// machine-user personal access token, or empty in tests that inject a connection.
func DialZitadel(_ context.Context, rawURL, pat string, extra ...grpc.DialOption) (*ZitadelBroker, error) {
	target, insecureOK, err := grpcTarget(rawURL)
	if err != nil {
		return nil, err
	}
	opts := []grpc.DialOption{grpc.WithPerRPCCredentials(patCredential{token: strings.TrimSpace(pat)})}
	if insecureOK {
		opts = append(opts, grpc.WithTransportCredentials(insecure.NewCredentials()))
	}
	opts = append(opts, extra...)
	conn, err := grpc.NewClient(target, opts...)
	if err != nil {
		return nil, err
	}
	return &ZitadelBroker{client: zitadelproto.NewSessionServiceClient(conn)}, nil
}

// BrokerFromEnv dials Zitadel when ERP_ZITADEL_URL is set. A missing PAT file
// leaves the broker unconfigured so the API can still boot in tests.
func BrokerFromEnv(ctx context.Context) (Broker, error) {
	raw := strings.TrimSpace(os.Getenv("ERP_ZITADEL_URL"))
	if raw == "" {
		return nil, nil
	}
	var pat string
	if path := strings.TrimSpace(os.Getenv("ERP_ZITADEL_PAT_FILE")); path != "" {
		b, err := os.ReadFile(path)
		if err != nil {
			return nil, err
		}
		pat = string(b)
	}
	return DialZitadel(ctx, raw, pat)
}

type patCredential struct{ token string }

// GetRequestMetadata attaches the personal access token.
func (p patCredential) GetRequestMetadata(context.Context, ...string) (map[string]string, error) {
	if p.token == "" {
		return map[string]string{}, nil
	}
	return map[string]string{"authorization": "Bearer " + p.token}, nil
}

// RequireTransportSecurity allows the local http Zitadel listener.
func (p patCredential) RequireTransportSecurity() bool { return false }

func grpcTarget(raw string) (string, bool, error) {
	u, err := url.Parse(raw)
	if err != nil {
		return "", false, err
	}
	return u.Host, u.Scheme == "http", nil
}

// Create opens a Zitadel session for a login name.
func (z *ZitadelBroker) Create(ctx context.Context, loginName string) (string, string, error) {
	resp, err := z.client.CreateSession(ctx, &zitadelproto.CreateSessionRequest{
		Checks: &zitadelproto.Checks{User: &zitadelproto.CheckUser{Search: &zitadelproto.CheckUser_LoginName{LoginName: loginName}}},
	})
	if err != nil {
		return "", "", mapStatus(err)
	}
	return resp.GetSessionId(), resp.GetSessionToken(), nil
}

// Password checks the password factor on an open Zitadel session.
func (z *ZitadelBroker) Password(ctx context.Context, sessionID, password string) (Factors, error) {
	_, err := z.client.SetSession(ctx, &zitadelproto.SetSessionRequest{
		SessionId: sessionID,
		Checks:    &zitadelproto.Checks{Password: &zitadelproto.CheckPassword{Password: password}},
	})
	if err != nil {
		return Factors{}, mapStatus(err)
	}
	return z.read(ctx, sessionID)
}

// TOTP checks the time-based one-time code on an open Zitadel session.
func (z *ZitadelBroker) TOTP(ctx context.Context, sessionID, code string) (Factors, error) {
	_, err := z.client.SetSession(ctx, &zitadelproto.SetSessionRequest{
		SessionId: sessionID,
		Checks:    &zitadelproto.Checks{Totp: &zitadelproto.CheckTOTP{Code: code}},
	})
	if err != nil {
		return Factors{}, mapStatus(err)
	}
	return z.read(ctx, sessionID)
}

// WebAuthN checks a credential assertion on an open Zitadel session.
func (z *ZitadelBroker) WebAuthN(ctx context.Context, sessionID string, assertion map[string]any) (Factors, error) {
	st, err := structpb.NewStruct(assertion)
	if err != nil {
		return Factors{}, ErrFactor
	}
	_, err = z.client.SetSession(ctx, &zitadelproto.SetSessionRequest{
		SessionId: sessionID,
		Checks:    &zitadelproto.Checks{WebAuthN: &zitadelproto.CheckWebAuthN{CredentialAssertionData: st}},
	})
	if err != nil {
		return Factors{}, mapStatus(err)
	}
	return z.read(ctx, sessionID)
}

func (z *ZitadelBroker) read(ctx context.Context, sessionID string) (Factors, error) {
	resp, err := z.client.GetSession(ctx, &zitadelproto.GetSessionRequest{SessionId: sessionID})
	if err != nil {
		return Factors{}, mapStatus(err)
	}
	f := resp.GetSession().GetFactors()
	out := Factors{UserID: f.GetUser().GetId(), LoginName: f.GetUser().GetLoginName(), DisplayName: f.GetUser().GetDisplayName()}
	if f.GetPassword().GetVerifiedAt() != nil {
		out.Password = true
	}
	if f.GetTotp().GetVerifiedAt() != nil {
		out.TOTP = true
	}
	if f.GetWebAuthN().GetVerifiedAt() != nil {
		out.WebAuthN = true
	}
	return out, nil
}

func mapStatus(err error) error {
	st, ok := status.FromError(err)
	if !ok {
		return err
	}
	msg := st.Message()
	if strings.Contains(msg, "COMMAND-JLK35") || strings.Contains(msg, "COMMAND-SFA3t") || strings.Contains(strings.ToLower(msg), "locked") {
		return ErrLocked
	}
	switch st.Code() {
	case codes.NotFound:
		return ErrUnknown
	case codes.Unauthenticated, codes.InvalidArgument, codes.PermissionDenied, codes.FailedPrecondition:
		return ErrFactor
	case codes.Unavailable, codes.DeadlineExceeded:
		return ErrUnavailable
	default:
		return err
	}
}
