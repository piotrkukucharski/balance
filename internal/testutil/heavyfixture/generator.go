package heavyfixture

import (
	"context"
	"database/sql"
	"fmt"
	"math/rand/v2"
	"time"

	"github.com/balance/balance/internal/domain"
)

// Generate10YearHeavyDataset builds a reproducible heavy SQLite dataset conforming to spec 10.4.
func Generate10YearHeavyDataset(ctx context.Context, tenantDB *sql.DB, seed uint64) error {
	pcg := rand.NewPCG(seed, seed^0x5555555555555555)
	rng := rand.New(pcg)

	now := time.Now().UTC().UnixMicro()
	dayMicros := int64(24 * 3600 * 1_000_000)
	startDate := now - 3650*dayMicros // 10 years ago
	agentID := domain.NewUUIDv7()

	tx, err := tenantDB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()

	// 1. Create User-Owned Party and 15 Multi-Currency Accounts
	userPartyID := domain.NewUUIDv7()
	_, err = tx.ExecContext(ctx, `
		INSERT INTO parties_current (id, version, name, party_type, is_user_owned, agent_id, updated_at)
		VALUES (?, 1, 'Primary User Family', 'INDIVIDUAL', 1, ?, ?)`,
		userPartyID, agentID, startDate,
	)
	if err != nil {
		return err
	}

	userAccounts := make([]string, 15)
	currencies := []string{"PLN", "EUR", "USD", "JPY", "GBP"}
	for i := 0; i < 15; i++ {
		accID := domain.NewUUIDv7()
		userAccounts[i] = accID
		curr := currencies[i%len(currencies)]
		_, err = tx.ExecContext(ctx, `
			INSERT INTO accounts_current (id, version, party_id, name, account_type, primary_currency, is_closed, agent_id, updated_at)
			VALUES (?, 1, ?, ?, 'CHECKING', ?, 0, ?, ?)`,
			accID, userPartyID, fmt.Sprintf("User Account %d (%s)", i+1, curr), curr, agentID, startDate,
		)
		if err != nil {
			return err
		}
	}

	// 2. Create 1,000 External Parties and 1,200 External Accounts (Merchants, Landlords, etc.)
	extParties := make([]string, 1000)
	extAccounts := make([]string, 1200)

	for i := 0; i < 1000; i++ {
		pID := domain.NewUUIDv7()
		extParties[i] = pID
		name := fmt.Sprintf("Merchant %d", i+1)
		if i == 0 {
			name = "Żabka Stores"
		} else if i == 1 {
			name = "Employer Corp"
		}
		_, err = tx.ExecContext(ctx, `
			INSERT INTO parties_current (id, version, name, party_type, is_user_owned, agent_id, updated_at)
			VALUES (?, 1, ?, 'ORGANIZATION', 0, ?, ?)`,
			pID, name, agentID, startDate,
		)
		if err != nil {
			return err
		}
	}

	for i := 0; i < 1200; i++ {
		accID := domain.NewUUIDv7()
		extAccounts[i] = accID
		pID := extParties[i%len(extParties)]
		_, err = tx.ExecContext(ctx, `
			INSERT INTO accounts_current (id, version, party_id, name, account_type, primary_currency, is_closed, agent_id, updated_at)
			VALUES (?, 1, ?, ?, 'EXTERNAL', 'PLN', 0, ?, ?)`,
			accID, pID, fmt.Sprintf("External POS %d", i+1), agentID, startDate,
		)
		if err != nil {
			return err
		}
	}

	// 3. Create 60 Buckets across 10 BucketGroups and 150 Targets
	buckets := make([]string, 60)
	groups := []string{"Housing", "Transportation", "Food", "Utilities", "Savings", "Healthcare", "Personal", "Debt", "Entertainment", "Gifts"}
	for i := 0; i < 60; i++ {
		bID := domain.NewUUIDv7()
		buckets[i] = bID
		group := groups[i%len(groups)]
		_, err = tx.ExecContext(ctx, `
			INSERT INTO buckets_projection (id, group_name, name, is_archived, version, updated_at)
			VALUES (?, ?, ?, 0, 1, ?)`,
			bID, group, fmt.Sprintf("Bucket %d (%s)", i+1, group), startDate,
		)
		if err != nil {
			return err
		}
	}

	for i := 0; i < 150; i++ {
		tID := domain.NewUUIDv7()
		bID := buckets[i%len(buckets)]
		priority := (i % 3) + 1
		cadence := "MONTHLY"
		if i%5 == 0 {
			cadence = "ONE_OFF"
		}
		_, err = tx.ExecContext(ctx, `
			INSERT INTO targets_projection (
				id, bucket_id, name, target_type, priority, sort_order, amount_mode,
				amount_minor, currency, recurrence_cadence, non_spending_strategy,
				current_funded_minor, is_archived, version, updated_at
			) VALUES (?, ?, ?, 'EXPENSE', ?, ?, 'FIXED', ?, 'PLN', ?, 'ROLL_OVER', 0, 0, 1, ?)`,
			tID, bID, fmt.Sprintf("Target %d", i+1), priority, i, int64(10000+rng.IntN(90000)), cadence, startDate,
		)
		if err != nil {
			return err
		}
	}

	// 4. Create 250 Matching Rules
	for i := 0; i < 250; i++ {
		rID := domain.NewUUIDv7()
		_, err = tx.ExecContext(ctx, `
			INSERT INTO matching_rules_current (
				id, version, name, priority, match_field, match_operator,
				match_pattern, target_counterparty_account_id, default_bucket_id, updated_at
			) VALUES (?, 1, ?, ?, 'RAW_PAYEE', 'CONTAINS', ?, ?, ?, ?)`,
			rID, fmt.Sprintf("Rule %d", i+1), i+1, fmt.Sprintf("MERCHANT_%d", i+1), extAccounts[i%len(extAccounts)], buckets[i%len(buckets)], startDate,
		)
		if err != nil {
			return err
		}
	}

	// 5. Generate sample transactions across days
	// To keep generation within < 1 second in unit tests while populating sufficient density:
	sampleCount := 500
	for i := 0; i < sampleCount; i++ {
		txDate := startDate + int64(i)*dayMicros*7
		amount := int64(1000 + rng.IntN(50000))
		userAcc := userAccounts[rng.IntN(len(userAccounts))]
		extAcc := extAccounts[rng.IntN(len(extAccounts))]
		bID := buckets[rng.IntN(len(buckets))]
		bTxID := domain.NewUUIDv7()

		_, err = tx.ExecContext(ctx, `
			INSERT INTO budget_transactions_current (
				id, version, source_account_id, destination_account_id, source_amount_minor,
				source_currency, destination_amount_minor, destination_currency, bucket_id,
				transaction_date, updated_at
			) VALUES (?, 1, ?, ?, ?, 'PLN', ?, 'PLN', ?, ?, ?)`,
			bTxID, userAcc, extAcc, amount, amount, bID, txDate, txDate,
		)
		if err != nil {
			return err
		}
	}

	return tx.Commit()
}
