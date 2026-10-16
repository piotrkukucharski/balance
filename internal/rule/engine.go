package rule

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"sync"
)

// Match operators
const (
	OpContains = "CONTAINS"
	OpPrefix   = "PREFIX"
	OpExact    = "EXACT"
	OpRegex    = "REGEX"
)

// Match fields
const (
	FieldRawPayee         = "RAW_PAYEE"
	FieldCounterpartyIBAN = "COUNTERPARTY_IBAN"
	FieldReference        = "REFERENCE"
	FieldAny              = "ANY"
)

// MatchingRule represents an auto-assignment rule.
type MatchingRule struct {
	ID                         string
	Name                       string
	Priority                   int
	MatchField                 string
	MatchOperator              string
	MatchPattern               string
	TargetCounterpartyAcctID   string
	DefaultBucketID            *string
	compiledRe                 *regexp.Regexp
}

// MatchingEngine evaluates raw transactions against priority-ordered rules.
type MatchingEngine struct {
	mu    sync.RWMutex
	rules []*MatchingRule
}

// NewMatchingEngine compiles and initializes the rules engine with ReDoS limits.
func NewMatchingEngine(rules []*MatchingRule) (*MatchingEngine, error) {
	engine := &MatchingEngine{}
	if err := engine.SetRules(rules); err != nil {
		return nil, err
	}
	return engine, nil
}

// SetRules validates regex patterns and caps rules count at 500.
func (e *MatchingEngine) SetRules(rules []*MatchingRule) error {
	if len(rules) > 500 {
		return errors.New("maximum active matching rules capped at 500")
	}

	for _, r := range rules {
		if len(r.MatchPattern) < 1 || len(r.MatchPattern) > 256 {
			return fmt.Errorf("rule %q match_pattern length must be between 1 and 256 characters", r.Name)
		}
		if r.MatchOperator == OpRegex {
			re, err := regexp.Compile(r.MatchPattern)
			if err != nil {
				return fmt.Errorf("invalid regex for rule %q: %w", r.Name, err)
			}
			r.compiledRe = re
		}
	}

	e.mu.Lock()
	defer e.mu.Unlock()
	e.rules = rules
	return nil
}

// MatchResult is returned when a rule matches an incoming raw transaction.
type MatchResult struct {
	RuleID                   string
	TargetCounterpartyAcctID string
	DefaultBucketID          *string
}

// Match evaluates incoming transaction attributes against the rules engine.
func (e *MatchingEngine) Match(rawPayee, counterpartyIBAN, reference string) *MatchResult {
	e.mu.RLock()
	defer e.mu.RUnlock()

	for _, r := range e.rules {
		if r.match(rawPayee, counterpartyIBAN, reference) {
			return &MatchResult{
				RuleID:                   r.ID,
				TargetCounterpartyAcctID: r.TargetCounterpartyAcctID,
				DefaultBucketID:          r.DefaultBucketID,
			}
		}
	}
	return nil
}

func (r *MatchingRule) match(rawPayee, counterpartyIBAN, reference string) bool {
	switch r.MatchField {
	case FieldCounterpartyIBAN:
		return r.evalString(counterpartyIBAN)
	case FieldRawPayee:
		return r.evalString(rawPayee)
	case FieldReference:
		return r.evalString(reference)
	case FieldAny:
		return r.evalString(rawPayee) || r.evalString(counterpartyIBAN) || r.evalString(reference)
	default:
		return false
	}
}

func (r *MatchingRule) evalString(target string) bool {
	if target == "" {
		return false
	}

	switch r.MatchOperator {
	case OpExact:
		return strings.EqualFold(target, r.MatchPattern)
	case OpPrefix:
		return strings.HasPrefix(strings.ToUpper(target), strings.ToUpper(r.MatchPattern))
	case OpContains:
		return strings.Contains(strings.ToUpper(target), strings.ToUpper(r.MatchPattern))
	case OpRegex:
		if r.compiledRe != nil {
			return r.compiledRe.MatchString(target)
		}
		return false
	default:
		return false
	}
}

// LoadRulesFromDB loads and compiles matching rules from matching_rules_current.
func LoadRulesFromDB(ctx context.Context, tenantDB *sql.DB) (*MatchingEngine, error) {
	rows, err := tenantDB.QueryContext(ctx, `
		SELECT id, name, priority, match_field, match_operator, match_pattern,
		       target_counterparty_account_id, default_bucket_id
		FROM matching_rules_current
		ORDER BY priority ASC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var rules []*MatchingRule
	for rows.Next() {
		var r MatchingRule
		var bucketID sql.NullString
		if err := rows.Scan(
			&r.ID, &r.Name, &r.Priority, &r.MatchField, &r.MatchOperator, &r.MatchPattern,
			&r.TargetCounterpartyAcctID, &bucketID,
		); err != nil {
			return nil, err
		}
		if bucketID.Valid {
			r.DefaultBucketID = &bucketID.String
		}
		rules = append(rules, &r)
	}

	return NewMatchingEngine(rules)
}
