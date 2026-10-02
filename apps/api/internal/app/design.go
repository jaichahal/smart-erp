package app

import (
	"context"
	"encoding/json"
	"net/http"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/jaichahal/smart-erp/apps/api/internal/kit/apierr"
	"github.com/jaichahal/smart-erp/apps/api/internal/kit/httpx"
)

// tokenDocument is the Phase 0 design-token set (06 "Component and token requirements").
// Light and dark share semantic roles. LTR and RTL share the component inventory so a
// direction switch does not drop a component.
func tokenDocument() map[string]any {
	components := []string{
		"status_chip", "money", "as_of_chip", "kpi_block", "spark_line", "bar", "line",
		"stacked_bar", "donut", "task_card", "approval_sheet", "list_row", "filter_bar",
		"period_picker", "data_grid", "command_palette", "form_field_set", "document_flow",
		"checklist_item", "empty_state", "error_state", "offline_banner",
	}
	semantic := map[string]string{
		"success": "#1B7F4E", "warning": "#B86E00", "critical": "#B42318",
		"info": "#175CD3", "neutral": "#475467",
	}
	darkSemantic := map[string]string{
		"success": "#3DDC84", "warning": "#F5A524", "critical": "#F97066",
		"info": "#84CAFF", "neutral": "#D0D5DD",
	}
	palette := []string{"#0072B2", "#E69F00", "#009E73", "#CC79A7", "#56B4E9", "#D55E00", "#F0E442", "#000000"}
	return map[string]any{
		"version": "1",
		"colour": map[string]any{
			"semantic": map[string]any{"light": semantic, "dark": darkSemantic},
			"data":     palette,
		},
		"type_scale": map[string]any{
			"latin":  []string{"12", "14", "16", "20", "24", "32"},
			"arabic": []string{"12", "14", "16", "20", "24", "32"},
		},
		"spacing":  []int{4, 8, 12, 16, 24, 32},
		"radius":   []int{4, 8, 12},
		"elevation": []int{0, 1, 2, 4},
		"motion_ms": []int{0, 120, 240},
		"direction": map[string]any{
			"ltr": map[string]any{"components": components},
			"rtl": map[string]any{"components": components},
		},
	}
}

func ensureDesignTokens(ctx context.Context, pool *pgxpool.Pool) error {
	if pool == nil {
		return nil
	}
	raw, err := json.Marshal(tokenDocument())
	if err != nil {
		return err
	}
	_, err = pool.Exec(ctx, `
		INSERT INTO erp.design_tokens (id, document) VALUES ('current', $1::jsonb)
		ON CONFLICT (id) DO UPDATE SET document = EXCLUDED.document`, raw)
	return err
}

func designTokens(pool *pgxpool.Pool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var raw []byte
		err := pool.QueryRow(r.Context(), `SELECT document FROM erp.design_tokens WHERE id = 'current'`).Scan(&raw)
		if err != nil {
			apierr.Write(w, r, apierr.Wrap(apierr.NotFound, "design tokens are not seeded", err))
			return
		}
		var doc any
		if err := json.Unmarshal(raw, &doc); err != nil {
			apierr.Write(w, r, apierr.Wrap(apierr.Internal, "design tokens are unreadable", err))
			return
		}
		httpx.JSON(w, r, http.StatusOK, doc)
	}
}
