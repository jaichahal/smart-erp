package notifications

import (
	"context"
	"net/http"

	"github.com/jaichahal/smart-erp/apps/api/internal/kit/apierr"
	"github.com/jaichahal/smart-erp/apps/api/internal/kit/ifmatch"
)

// ApproveFromShade refetches the live document and refuses to decide when the
// caller's state_version no longer matches (E10, R13.4).
func (s *Service) ApproveFromShade(ctx context.Context, r *http.Request, id, action, reason string) (LiveDocument, error) {
	if s.Approvals == nil {
		return LiveDocument{}, apierr.New(apierr.Internal, text(requestLang(r), "validation.event"))
	}
	expected, _, err := ifmatch.Parse(r, true)
	if err != nil {
		return LiveDocument{}, err
	}
	doc, err := s.Approvals.Fetch(ctx, id)
	if err != nil {
		return LiveDocument{}, err
	}
	if err := ifmatch.Check(expected, doc.StateVersion, doc); err != nil {
		return doc, err
	}
	if err := s.Approvals.Decide(ctx, id, expected, action, reason); err != nil {
		return doc, err
	}
	return doc, nil
}
