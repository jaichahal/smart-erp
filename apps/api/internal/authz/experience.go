package authz

import (
	"context"

	"github.com/jackc/pgx/v5"

	"github.com/jaichahal/smart-erp/apps/api/internal/kit/rls"
)

// Experience is the persona, tabs, and endpoints a token's roles resolve to.
type Experience struct {
	Personas            []string
	PrivilegedTabs      []string
	PrivilegedEndpoints []string
	allowed             map[string]struct{}
}

// LeastPrivileged reports the fail-closed persona (A13, R1.12).
func (e Experience) LeastPrivileged() bool {
	return len(e.Personas) == 1 && e.Personas[0] == "least_privileged"
}

// Allows reports whether this experience may call method and path.
func (e Experience) Allows(method, path string) bool {
	_, ok := e.allowed[method+" "+path]
	return ok
}

// ResolveExperience maps token roles onto personas. An unrecognised role, or
// no role at all, becomes the least-privileged persona and no privileged tab
// or endpoint (A13, R1.12).
func (s *Service) ResolveExperience(ctx context.Context, actor rls.Principal) (Experience, error) {
	var exp Experience
	err := rls.Tx(ctx, s.pool, actor, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `SELECT DISTINCT persona FROM erp.roles WHERE name = ANY($1) ORDER BY persona`, actor.Roles)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var persona string
			if err := rows.Scan(&persona); err != nil {
				return err
			}
			exp.Personas = append(exp.Personas, persona)
		}
		if err := rows.Err(); err != nil {
			return err
		}
		if len(exp.Personas) == 0 {
			var least string
			if err := tx.QueryRow(ctx, `SELECT name FROM erp.personas WHERE least_privileged ORDER BY name LIMIT 1`).Scan(&least); err != nil {
				return err
			}
			exp.Personas = []string{least}
		}
		tabs, err := tx.Query(ctx, `
			SELECT DISTINCT tab FROM erp.persona_tabs
			WHERE persona = ANY($1) AND privileged
			ORDER BY tab`, exp.Personas)
		if err != nil {
			return err
		}
		defer tabs.Close()
		for tabs.Next() {
			var tab string
			if err := tabs.Scan(&tab); err != nil {
				return err
			}
			exp.PrivilegedTabs = append(exp.PrivilegedTabs, tab)
		}
		if err := tabs.Err(); err != nil {
			return err
		}
		ends, err := tx.Query(ctx, `
			SELECT DISTINCT e.method, e.path, e.privileged
			FROM erp.endpoint_permissions e
			JOIN erp.role_permissions rp ON rp.permission = e.permission AND rp.role_name = ANY($1)
			ORDER BY e.method, e.path`, actor.Roles)
		if err != nil {
			return err
		}
		defer ends.Close()
		exp.allowed = map[string]struct{}{}
		for ends.Next() {
			var method, path string
			var privileged bool
			if err := ends.Scan(&method, &path, &privileged); err != nil {
				return err
			}
			exp.allowed[method+" "+path] = struct{}{}
			if privileged {
				exp.PrivilegedEndpoints = append(exp.PrivilegedEndpoints, method+" "+path)
			}
		}
		return ends.Err()
	})
	return exp, err
}
