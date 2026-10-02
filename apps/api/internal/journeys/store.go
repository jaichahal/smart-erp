package journeys

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/jaichahal/smart-erp/apps/api/internal/kit/apierr"
)

type querier interface {
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
}

func loadDefinition(ctx context.Context, q querier, slug string) (definition, error) {
	var d definition
	err := q.QueryRow(ctx, `SELECT slug, title_key, group_key, personas FROM erp.journey_definitions WHERE slug = $1`, slug).
		Scan(&d.Slug, &d.TitleKey, &d.GroupKey, &d.Personas)
	if errors.Is(err, pgx.ErrNoRows) {
		return definition{}, apierr.New(apierr.NotFound, text("en", "journey.not_found"))
	}
	if err != nil {
		return definition{}, fmt.Errorf("journeys: load definition: %w", err)
	}
	steps, err := loadSteps(ctx, q, slug)
	if err != nil {
		return definition{}, err
	}
	if len(steps) == 0 {
		return definition{}, fmt.Errorf("journeys: definition %s has no steps", slug)
	}
	d.Steps = steps
	return d, nil
}

func loadSteps(ctx context.Context, q querier, slug string) ([]stepDef, error) {
	rows, err := q.Query(ctx, `SELECT step_id, position, kind, title_key, input_schema, guard
		FROM erp.journey_definition_steps WHERE slug = $1 ORDER BY position`, slug)
	if err != nil {
		return nil, fmt.Errorf("journeys: load steps: %w", err)
	}
	defer rows.Close()
	var out []stepDef
	for rows.Next() {
		var s stepDef
		var schema, guard []byte
		if err := rows.Scan(&s.ID, &s.Position, &s.Kind, &s.TitleKey, &schema, &guard); err != nil {
			return nil, fmt.Errorf("journeys: scan step: %w", err)
		}
		s.InputSchema = map[string]any{}
		s.Guard = map[string]any{}
		if err := json.Unmarshal(schema, &s.InputSchema); err != nil {
			return nil, fmt.Errorf("journeys: input schema: %w", err)
		}
		if err := json.Unmarshal(guard, &s.Guard); err != nil {
			return nil, fmt.Errorf("journeys: guard: %w", err)
		}
		out = append(out, s)
	}
	return out, rows.Err()
}

func listDefinitions(ctx context.Context, q querier, personas []string, filter string) ([]definition, error) {
	if len(personas) == 0 {
		return nil, nil
	}
	rows, err := q.Query(ctx, `SELECT slug FROM erp.journey_definitions
		WHERE personas && $1::text[]
		  AND ($2 = '' OR $2 = ANY(personas))
		ORDER BY slug`, personas, filter)
	if err != nil {
		return nil, fmt.Errorf("journeys: list definitions: %w", err)
	}
	defer rows.Close()
	var slugs []string
	for rows.Next() {
		var slug string
		if err := rows.Scan(&slug); err != nil {
			return nil, err
		}
		slugs = append(slugs, slug)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	var out []definition
	for _, slug := range slugs {
		d, err := loadDefinition(ctx, q, slug)
		if err != nil {
			return nil, err
		}
		out = append(out, d)
	}
	return out, nil
}

func loadInstance(ctx context.Context, q querier, id uuid.UUID, forUpdate bool) (instance, error) {
	sql := `SELECT id, company_id, slug, user_id, persona, status, current_step_id, server_state, state_version, updated_at
		FROM erp.journey_instances WHERE id = $1`
	if forUpdate {
		sql += ` FOR UPDATE`
	}
	var inst instance
	var state []byte
	err := q.QueryRow(ctx, sql, id).Scan(&inst.ID, &inst.CompanyID, &inst.Slug, &inst.UserID, &inst.Persona,
		&inst.Status, &inst.CurrentStepID, &state, &inst.StateVersion, &inst.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return instance{}, apierr.New(apierr.NotFound, text("en", "journey.not_found"))
	}
	if err != nil {
		return instance{}, fmt.Errorf("journeys: load instance: %w", err)
	}
	inst.ServerState = map[string]any{}
	if err := json.Unmarshal(state, &inst.ServerState); err != nil {
		return instance{}, fmt.Errorf("journeys: server state: %w", err)
	}
	return inst, nil
}

func insertInstance(ctx context.Context, tx pgx.Tx, inst instance) error {
	_, err := tx.Exec(ctx, `INSERT INTO erp.journey_instances
		(id, company_id, slug, user_id, persona, status, current_step_id, server_state, state_version)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9)`,
		inst.ID, inst.CompanyID, inst.Slug, inst.UserID, inst.Persona, inst.Status, inst.CurrentStepID,
		mustJSON(inst.ServerState), inst.StateVersion)
	if err != nil {
		return fmt.Errorf("journeys: insert instance: %w", err)
	}
	return nil
}

func saveInstance(ctx context.Context, tx pgx.Tx, inst instance) error {
	tag, err := tx.Exec(ctx, `UPDATE erp.journey_instances
		SET status = $2, current_step_id = $3, server_state = $4, state_version = $5, updated_at = clock_timestamp()
		WHERE id = $1`, inst.ID, inst.Status, inst.CurrentStepID, mustJSON(inst.ServerState), inst.StateVersion)
	if err != nil {
		return fmt.Errorf("journeys: save instance: %w", err)
	}
	if tag.RowsAffected() != 1 {
		return fmt.Errorf("journeys: save instance affected %d rows", tag.RowsAffected())
	}
	return nil
}

func insertTransition(ctx context.Context, tx pgx.Tx, inst instance, stepID, outcome string, input map[string]any, result []byte) error {
	_, err := tx.Exec(ctx, `INSERT INTO erp.journey_transitions
		(id, company_id, instance_id, step_id, outcome, input, result, state_version)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8)`,
		uuid.New(), inst.CompanyID, inst.ID, stepID, outcome, mustJSON(input), result, inst.StateVersion)
	if err != nil {
		return fmt.Errorf("journeys: insert transition: %w", err)
	}
	return nil
}
