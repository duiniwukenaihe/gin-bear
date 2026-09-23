//go:build !bear_no_casbin

package bear

import (
	"database/sql"
	"errors"
	"fmt"
	"strings"

	"github.com/casbin/casbin/v2/model"
	"github.com/casbin/casbin/v2/persist"
)

// PostgresCasbinAdapter stores policies in the existing casbin_rule table using
// an application-owned *sql.DB. Schema creation is a separate migration step.
type PostgresCasbinAdapter struct {
	db *sql.DB
}

var _ persist.Adapter = (*PostgresCasbinAdapter)(nil)

func NewPostgresCasbinAdapter(db *sql.DB) (*PostgresCasbinAdapter, error) {
	if db == nil {
		return nil, errors.New("PostgreSQL Casbin adapter requires a database pool")
	}
	return &PostgresCasbinAdapter{db: db}, nil
}

func (a *PostgresCasbinAdapter) LoadPolicy(m model.Model) error {
	rows, err := a.db.Query("SELECT ptype, v0, v1, v2, v3, v4, v5 FROM casbin_rule ORDER BY id")
	if err != nil {
		return fmt.Errorf("load casbin_rule: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var ptype string
		var stored [6]sql.NullString
		if err := rows.Scan(&ptype, &stored[0], &stored[1], &stored[2], &stored[3], &stored[4], &stored[5]); err != nil {
			return fmt.Errorf("scan casbin_rule: %w", err)
		}
		if ptype == "" {
			return errors.New("casbin_rule contains an empty policy type")
		}
		policy := make([]string, 7)
		policy[0] = ptype
		for index, value := range stored {
			policy[index+1] = value.String
		}
		for len(policy) > 1 && policy[len(policy)-1] == "" {
			policy = policy[:len(policy)-1]
		}
		if err := persist.LoadPolicyArray(policy, m); err != nil {
			return fmt.Errorf("decode casbin_rule: %w", err)
		}
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("read casbin_rule: %w", err)
	}
	return nil
}

func (a *PostgresCasbinAdapter) SavePolicy(m model.Model) error {
	tx, err := a.db.Begin()
	if err != nil {
		return fmt.Errorf("begin casbin policy replacement: %w", err)
	}
	defer tx.Rollback()
	if _, err := tx.Exec("DELETE FROM casbin_rule"); err != nil {
		return fmt.Errorf("clear casbin_rule: %w", err)
	}
	for _, section := range []string{"p", "g"} {
		for ptype, assertion := range m[section] {
			for _, rule := range assertion.Policy {
				values, err := casbinRuleValues(section, ptype, rule)
				if err != nil {
					return err
				}
				if _, err := tx.Exec(casbinInsertSQL, values...); err != nil {
					return fmt.Errorf("save casbin_rule: %w", err)
				}
			}
		}
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit casbin policy replacement: %w", err)
	}
	return nil
}

const casbinInsertSQL = "INSERT INTO casbin_rule (ptype, v0, v1, v2, v3, v4, v5) VALUES ($1, $2, $3, $4, $5, $6, $7)"

func casbinRuleValues(sec, ptype string, rule []string) ([]any, error) {
	if ptype == "" || !strings.HasPrefix(ptype, sec) || (sec != "p" && sec != "g") {
		return nil, fmt.Errorf("invalid Casbin policy type %q for section %q", ptype, sec)
	}
	if len(rule) > 6 {
		return nil, fmt.Errorf("casbin policy %q has %d fields, maximum is 6", ptype, len(rule))
	}
	values := make([]any, 7)
	values[0] = ptype
	for index := 0; index < 6; index++ {
		values[index+1] = ""
		if index < len(rule) {
			values[index+1] = rule[index]
		}
	}
	return values, nil
}

func (a *PostgresCasbinAdapter) AddPolicy(sec, ptype string, rule []string) error {
	values, err := casbinRuleValues(sec, ptype, rule)
	if err != nil {
		return err
	}
	if _, err := a.db.Exec(casbinInsertSQL, values...); err != nil {
		return fmt.Errorf("add casbin_rule: %w", err)
	}
	return nil
}

func (a *PostgresCasbinAdapter) RemovePolicy(sec, ptype string, rule []string) error {
	values, err := casbinRuleValues(sec, ptype, rule)
	if err != nil {
		return err
	}
	const query = "DELETE FROM casbin_rule WHERE ptype = $1 AND COALESCE(v0, '') = $2 AND COALESCE(v1, '') = $3 AND COALESCE(v2, '') = $4 AND COALESCE(v3, '') = $5 AND COALESCE(v4, '') = $6 AND COALESCE(v5, '') = $7"
	if _, err := a.db.Exec(query, values...); err != nil {
		return fmt.Errorf("remove casbin_rule: %w", err)
	}
	return nil
}

func (a *PostgresCasbinAdapter) RemoveFilteredPolicy(sec, ptype string, fieldIndex int, fieldValues ...string) error {
	if ptype == "" || !strings.HasPrefix(ptype, sec) || (sec != "p" && sec != "g") {
		return fmt.Errorf("invalid Casbin policy type %q for section %q", ptype, sec)
	}
	if fieldIndex < -1 || fieldIndex > 5 || (fieldIndex == -1 && len(fieldValues) != 0) || (fieldIndex >= 0 && fieldIndex+len(fieldValues) > 6) {
		return fmt.Errorf("invalid Casbin filter index %d with %d fields", fieldIndex, len(fieldValues))
	}
	query := "DELETE FROM casbin_rule WHERE ptype = $1"
	args := []any{ptype}
	if fieldIndex >= 0 {
		for offset, value := range fieldValues {
			if value == "" {
				continue
			}
			args = append(args, value)
			query += fmt.Sprintf(" AND v%d = $%d", fieldIndex+offset, len(args))
		}
	}
	if _, err := a.db.Exec(query, args...); err != nil {
		return fmt.Errorf("remove filtered casbin_rule: %w", err)
	}
	return nil
}
