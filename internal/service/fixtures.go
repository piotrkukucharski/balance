package service

import (
	"context"
	"fmt"
	"math/rand"
	"time"

	"github.com/balance/balance/internal/budget"
	"github.com/balance/balance/internal/clock"
	"github.com/balance/balance/internal/domain"
)

// FixtureProgressReporter is a callback interface for displaying progress without polluting stdout with raw logs.
type FixtureProgressReporter interface {
	StartStep(name string, totalItems int)
	Advance(step int)
	CompleteStep(summary string)
}

// LoadFixturesInput defines parameters for generating rich fixtures.
type LoadFixturesInput struct {
	BudgetID        string
	UserID          string
	LastTransaction time.Time
	FirstActionAt   time.Time
	CorrelationID   string
	Reporter        FixtureProgressReporter
}

// LoadFixtures populates accounts, buckets, targets and transactions conforming to all user requirements.
func (s *AppService) LoadFixtures(ctx context.Context, input LoadFixturesInput) error {
	// 1. Validate owner role
	isOwner, err := s.CheckOwnerMembership(ctx, input.BudgetID, input.UserID)
	if err != nil {
		return fmt.Errorf("verify budget membership: %w", err)
	}
	if !isOwner {
		return fmt.Errorf("user %s is not OWNER of budget %s", input.UserID, input.BudgetID)
	}

	// 2. Validate at least 100 days difference
	daysDiff := int(input.LastTransaction.Sub(input.FirstActionAt).Hours() / 24)
	if daysDiff < 100 {
		return fmt.Errorf("difference between firstActionAt (%s) and lastTransaction (%s) must be at least 100 days (got %d days)",
			input.FirstActionAt.Format("2006-01-02"), input.LastTransaction.Format("2006-01-02"), daysDiff)
	}

	corrID := input.CorrelationID
	if corrID == "" {
		corrID = domain.NewUUIDv7()
	}

	// Use ManualClock initialized at firstActionAt to advance time across operations
	simClock := clock.NewManualClock(input.FirstActionAt, time.UTC)
	s.SetClock(simClock)

	rep := input.Reporter

	// Step 1: Create 4 financial accounts
	if rep != nil {
		rep.StartStep("Creating financial accounts (a, b, c, d)", 4)
	}

	// a: Main account (PLN)
	mainAcctID, err := s.CreateFinancialAccount(ctx, CreateAccountInput{
		BudgetID:            input.BudgetID,
		UserID:              input.UserID,
		CorrelationID:       corrID,
		Name:                "Main account",
		AccountType:         "CHECKING",
		PrimaryCurrency:     "PLN",
		IBAN:                "PL61109010140000071219812874",
		FaviconType:         "EMOJI",
		FaviconValue:        "🏦",
		InitialBalanceMinor: 200000, // 2,000.00 PLN initial balance
		ActionAt:            &input.FirstActionAt,
	})
	if err != nil {
		return fmt.Errorf("create main account: %w", err)
	}
	if rep != nil {
		rep.Advance(1)
	}

	// b: Wallet (PLN)
	walletAcctID, err := s.CreateFinancialAccount(ctx, CreateAccountInput{
		BudgetID:            input.BudgetID,
		UserID:              input.UserID,
		CorrelationID:       corrID,
		Name:                "Wallet",
		AccountType:         "CASH",
		PrimaryCurrency:     "PLN",
		FaviconType:         "EMOJI",
		FaviconValue:        "💵",
		InitialBalanceMinor: 30000, // 300.00 PLN
		ActionAt:            &input.FirstActionAt,
	})
	if err != nil {
		return fmt.Errorf("create wallet: %w", err)
	}
	if rep != nil {
		rep.Advance(1)
	}

	// c: EUR account
	eurAcctID, err := s.CreateFinancialAccount(ctx, CreateAccountInput{
		BudgetID:            input.BudgetID,
		UserID:              input.UserID,
		CorrelationID:       corrID,
		Name:                "EUR account",
		AccountType:         "SAVINGS",
		PrimaryCurrency:     "EUR",
		IBAN:                "PL82102052260000610204177890",
		FaviconType:         "EMOJI",
		FaviconValue:        "💶",
		InitialBalanceMinor: 100000, // 1,000.00 EUR
		ActionAt:            &input.FirstActionAt,
	})
	if err != nil {
		return fmt.Errorf("create EUR account: %w", err)
	}
	if rep != nil {
		rep.Advance(1)
	}

	// d: USD account
	usdAcctID, err := s.CreateFinancialAccount(ctx, CreateAccountInput{
		BudgetID:            input.BudgetID,
		UserID:              input.UserID,
		CorrelationID:       corrID,
		Name:                "USD account",
		AccountType:         "SAVINGS",
		PrimaryCurrency:     "USD",
		FaviconType:         "EMOJI",
		FaviconValue:        "💵",
		InitialBalanceMinor: 50000, // 500.00 USD
		ActionAt:            &input.FirstActionAt,
	})
	if err != nil {
		return fmt.Errorf("create USD account: %w", err)
	}
	if rep != nil {
		rep.Advance(1)
		rep.CompleteStep("Created 4 accounts (Main account PLN, Wallet PLN, EUR account, USD account)")
	}

	// Step 2: Create at least 15 realistic budget buckets across functional groups
	type bucketDef struct {
		groupName   string
		bucketName  string
		targetName  string
		targetType  string
		amount      int64
		ceiling     *int64
		cadence     string
		strategy    string
	}

	ptrInt64 := func(v int64) *int64 { return &v }

	bucketDefs := []bucketDef{
		{"Oszczędności", "Poduszka bezpieczeństwa", "Rezerwa awaryjna 6M", "SAVINGS", 100000, ptrInt64(2000000), budget.CadenceMonthly, budget.StrategyCapAndHold}, // Goal 20,000 PLN, monthly 1,000 PLN
		{"Oszczędności", "Wakacje", "Letni wyjazd wakacyjny", "SAVINGS", 60000, ptrInt64(800000), budget.CadenceMonthly, budget.StrategyCapAndHold},                 // Goal 8,000 PLN, monthly 600 PLN
		{"Oszczędności", "Zakup samochodu", "Wkład własny na auto", "SAVINGS", 150000, ptrInt64(3500000), budget.CadenceMonthly, budget.StrategyCapAndHold},          // Goal 35,000 PLN, monthly 1,500 PLN
		{"Rachunki", "Czynsz", "Opłata za mieszkanie", "EXPENSE", 280000, nil, budget.CadenceMonthly, budget.StrategyRollOver},
		{"Rachunki", "Media i prąd", "Energia elektryczna i gaz", "EXPENSE", 45000, nil, budget.CadenceMonthly, budget.StrategyRollOver},
		{"Rachunki", "Internet i telefon", "Światłowód i abonament", "EXPENSE", 12000, nil, budget.CadenceMonthly, budget.StrategyRollOver},
		{"Jedzenie", "Zakupy spożywcze", "Tygodniowy koszyk żywności", "EXPENSE", 160000, nil, budget.CadenceMonthly, budget.StrategyRollOver},
		{"Jedzenie", "Restauracje i wyjścia", "Posiłki na mieście i kawa", "EXPENSE", 40000, nil, budget.CadenceMonthly, budget.StrategyRollOver},
		{"Transport", "Paliwo i transport", "Benzyna i bilety komunikacji", "EXPENSE", 50000, nil, budget.CadenceMonthly, budget.StrategyRollOver},
		{"Zdrowie", "Zdrowie i leki", "Apteka i profilaktyka", "EXPENSE", 25000, nil, budget.CadenceMonthly, budget.StrategyRollOver},
		{"Edukacja", "Zajęcia sportowe", "Karnet i basen", "EXPENSE", 30000, nil, budget.CadenceMonthly, budget.StrategyRollOver},
		{"Rozwój", "Nauka i kursy", "Lekcje językowe i mentoring", "EXPENSE", 35000, nil, budget.CadenceMonthly, budget.StrategyRollOver},
		{"Styl życia", "Subskrypcje", "Konto streamingowe i chmura", "EXPENSE", 8000, nil, budget.CadenceMonthly, budget.StrategyRollOver},
		{"Styl życia", "Rozrywka i hobby", "Kino, książki i gry", "EXPENSE", 25000, nil, budget.CadenceMonthly, budget.StrategyRollOver},
		{"Styl życia", "Ubrania i obuwie", "Garderoba sezonowa", "EXPENSE", 30000, nil, budget.CadenceMonthly, budget.StrategyRollOver},
		{"Nieprzewidziane", "Drobne naprawy", "Serwis domowy i narzędzia", "EXPENSE", 20000, ptrInt64(300000), budget.CadenceMonthly, budget.StrategyCapAndHold}, // Goal 3,000 PLN cap
	}

	if rep != nil {
		rep.StartStep("Creating budget envelopes & targets (16 buckets)", len(bucketDefs))
	}

	bucketMap := make(map[string]string) // name -> bucketId
	for idx, b := range bucketDefs {
		bID, err := s.CreateBucket(ctx, input.BudgetID, budget.CreateBucketInput{
			GroupName:     b.groupName,
			Name:          b.bucketName,
			AgentID:       input.UserID,
			CorrelationID: corrID,
		})
		if err != nil {
			return fmt.Errorf("create bucket %s: %w", b.bucketName, err)
		}
		bucketMap[b.bucketName] = bID

		tType := b.targetType
		if tType == "" {
			tType = "EXPENSE"
		}
		strat := b.strategy
		if strat == "" {
			strat = budget.StrategyRollOver
		}

		_, err = s.CreateTarget(ctx, input.BudgetID, budget.CreateTargetInput{
			BucketID:            bID,
			Name:                b.targetName,
			TargetType:          tType,
			Priority:            2,
			SortOrder:           idx + 1,
			AmountMinor:         b.amount,
			Currency:            "PLN",
			RecurrenceCadence:   b.cadence,
			NonSpendingStrategy: strat,
			CeilingAmountMinor:  b.ceiling,
			AgentID:             input.UserID,
			CorrelationID:       corrID,
		})
		if err != nil {
			return fmt.Errorf("create target for bucket %s: %w", b.bucketName, err)
		}
		if rep != nil {
			rep.Advance(1)
		}
	}
	if rep != nil {
		rep.CompleteStep(fmt.Sprintf("Created %d buckets with monthly funding targets", len(bucketDefs)))
	}

	// Step 3: Generate realistic transactions iterating day-by-day with ManualClock
	if rep != nil {
		rep.StartStep(fmt.Sprintf("Simulating realistic cashflow over %d days", daysDiff), daysDiff)
	}

	rng := rand.New(rand.NewSource(42)) // Deterministic random seed for consistent fixture tests

	// Initial date setup
	currDate := input.FirstActionAt
	lastSalaryMonth := -1
	annualSubscriptionBilled := false

	daysCount := 0
	for !currDate.After(input.LastTransaction) {
		daysCount++
		simClock.SetDateTime(currDate)

		// 1. Monthly salary arriving to Main account (PLN) around start of month (e.g. day 1-5)
		if currDate.Month() != time.Month(lastSalaryMonth) {
			salaryDay := 1 + (rng.Intn(4)) // Day 1 to 4 of month
			if currDate.Day() >= salaryDay {
				lastSalaryMonth = int(currDate.Month())
				// Salary: 11,500.00 PLN + slight bonus
				salaryAmountMinor := int64(1150000 + rng.Intn(100000))
				_, err = s.AddTransaction(ctx, AddTransactionInput{
					BudgetID:          input.BudgetID,
					UserID:            input.UserID,
					CorrelationID:     corrID,
					TxType:            TxIncome,
					DestinationAcctID: mainAcctID,
					AmountMinor:       salaryAmountMinor,
					Currency:          "PLN",
					Notes:             "Tech Innovations Sp. z o.o. - Wynagrodzenie",
					TransactionDate:   currDate,
				})
				if err != nil {
					return fmt.Errorf("salary transaction on %s: %w", currDate.Format("2006-01-02"), err)
				}

				// Transfers from Main account to other accounts after salary
				// 1. To Wallet (CASH): 500 PLN
				_, _ = s.AddTransaction(ctx, AddTransactionInput{
					BudgetID:          input.BudgetID,
					UserID:            input.UserID,
					CorrelationID:     corrID,
					TxType:            TxTransfer,
					SourceAccountID:   mainAcctID,
					DestinationAcctID: walletAcctID,
					AmountMinor:       50000,
					Currency:          "PLN",
					Notes:             "Bankomat - wypłata gotówki do portfela",
					TransactionDate:   currDate,
				})

				// 2. To EUR account: 300 EUR (approx 1,300 PLN)
				_, _ = s.AddTransaction(ctx, AddTransactionInput{
					BudgetID:          input.BudgetID,
					UserID:            input.UserID,
					CorrelationID:     corrID,
					TxType:            TxTransfer,
					SourceAccountID:   mainAcctID,
					DestinationAcctID: eurAcctID,
					AmountMinor:       130000,
					Currency:          "PLN",
					DestAmountMinor:   30000,
					DestCurrency:      "EUR",
					Notes:             "Przelew oszczędnościowy EUR",
					TransactionDate:   currDate,
				})

				// 3. To USD account: 200 USD (approx 800 PLN)
				_, _ = s.AddTransaction(ctx, AddTransactionInput{
					BudgetID:          input.BudgetID,
					UserID:            input.UserID,
					CorrelationID:     corrID,
					TxType:            TxTransfer,
					SourceAccountID:   mainAcctID,
					DestinationAcctID: usdAcctID,
					AmountMinor:       80000,
					Currency:          "PLN",
					DestAmountMinor:   20000,
					DestCurrency:      "USD",
					Notes:             "Przelew oszczędnościowy USD",
					TransactionDate:   currDate,
				})
			}
		}

		// 2. Monthly fixed bills (Rent, Utilities, Internet) around day 5-10
		if currDate.Day() == 8 {
			rentBucket := bucketMap["Czynsz"]
			_, _ = s.AddTransaction(ctx, AddTransactionInput{
				BudgetID:        input.BudgetID,
				UserID:          input.UserID,
				CorrelationID:   corrID,
				TxType:          TxExpense,
				SourceAccountID: mainAcctID,
				AmountMinor:     280000,
				Currency:        "PLN",
				BucketID:        &rentBucket,
				Notes:           "Wspólnota Mieszkaniowa - Czynsz",
				TransactionDate: currDate,
			})

			utilitiesBucket := bucketMap["Media i prąd"]
			_, _ = s.AddTransaction(ctx, AddTransactionInput{
				BudgetID:        input.BudgetID,
				UserID:          input.UserID,
				CorrelationID:   corrID,
				TxType:          TxExpense,
				SourceAccountID: mainAcctID,
				AmountMinor:     38500 + int64(rng.Intn(8000)),
				Currency:        "PLN",
				BucketID:        &utilitiesBucket,
				Notes:           "PGE Polska Grupa Energetyczna",
				TransactionDate: currDate,
			})
		}
		if currDate.Day() == 12 {
			netBucket := bucketMap["Internet i telefon"]
			_, _ = s.AddTransaction(ctx, AddTransactionInput{
				BudgetID:        input.BudgetID,
				UserID:          input.UserID,
				CorrelationID:   corrID,
				TxType:          TxExpense,
				SourceAccountID: mainAcctID,
				AmountMinor:     11999,
				Currency:        "PLN",
				BucketID:        &netBucket,
				Notes:           "Orange Polska Światłowód",
				TransactionDate: currDate,
			})
		}

		// 3. Weekly recurring activity (zajęcia raz w tygodniu - every Tuesday)
		if currDate.Weekday() == time.Tuesday {
			sportBucket := bucketMap["Zajęcia sportowe"]
			_, _ = s.AddTransaction(ctx, AddTransactionInput{
				BudgetID:        input.BudgetID,
				UserID:          input.UserID,
				CorrelationID:   corrID,
				TxType:          TxExpense,
				SourceAccountID: mainAcctID,
				AmountMinor:     7500, // 75 PLN
				Currency:        "PLN",
				BucketID:        &sportBucket,
				Notes:           "Basen i Trening Personalny",
				TransactionDate: currDate,
			})
		}

		// 4. Recurring activity every 18 days (zajęcia raz na 18 dni)
		if daysCount%18 == 0 {
			courseBucket := bucketMap["Nauka i kursy"]
			_, _ = s.AddTransaction(ctx, AddTransactionInput{
				BudgetID:        input.BudgetID,
				UserID:          input.UserID,
				CorrelationID:   corrID,
				TxType:          TxExpense,
				SourceAccountID: mainAcctID,
				AmountMinor:     18000, // 180 PLN
				Currency:        "PLN",
				BucketID:        &courseBucket,
				Notes:           "Warsztaty specjalistyczne i mentoring (co 18 dni)",
				TransactionDate: currDate,
			})
		}

		// 5. Monthly subscriptions (e.g. day 15)
		if currDate.Day() == 15 {
			subBucket := bucketMap["Subskrypcje"]
			// Spotify / Netflix
			_, _ = s.AddTransaction(ctx, AddTransactionInput{
				BudgetID:        input.BudgetID,
				UserID:          input.UserID,
				CorrelationID:   corrID,
				TxType:          TxExpense,
				SourceAccountID: mainAcctID,
				AmountMinor:     4999,
				Currency:        "PLN",
				BucketID:        &subBucket,
				Notes:           "Netflix / Spotify Family",
				TransactionDate: currDate,
			})
			// Cloud Storage in USD from USD account
			_, _ = s.AddTransaction(ctx, AddTransactionInput{
				BudgetID:        input.BudgetID,
				UserID:          input.UserID,
				CorrelationID:   corrID,
				TxType:          TxExpense,
				SourceAccountID: usdAcctID,
				AmountMinor:     1200, // $12.00
				Currency:        "USD",
				BucketID:        &subBucket,
				Notes:           "GitHub & Cloud Services USD",
				TransactionDate: currDate,
			})
		}

		// 6. Annual subscription (once per year)
		if !annualSubscriptionBilled && currDate.Month() == time.May && currDate.Day() == 20 {
			annualSubscriptionBilled = true
			subBucket := bucketMap["Subskrypcje"]
			_, _ = s.AddTransaction(ctx, AddTransactionInput{
				BudgetID:        input.BudgetID,
				UserID:          input.UserID,
				CorrelationID:   corrID,
				TxType:          TxExpense,
				SourceAccountID: mainAcctID,
				AmountMinor:     34900, // 349 PLN
				Currency:        "PLN",
				BucketID:        &subBucket,
				Notes:           "Roczna subskrypcja JetBrains / 1Password",
				TransactionDate: currDate,
			})
		}

		// 7. Frequent everyday expenses (Groceries, Fuel, Dining, Pharmacy)
		// Groceries (Biedronka / Lidl) 2-3 times a week
		if rng.Float32() < 0.45 {
			foodBucket := bucketMap["Zakupy spożywcze"]
			payee := "Biedronka"
			if rng.Float32() < 0.5 {
				payee = "Lidl"
			}
			amt := int64(4500 + rng.Intn(16000))
			// Alternate payment source between Main account and cash Wallet
			acct := mainAcctID
			if rng.Float32() < 0.25 {
				acct = walletAcctID
			}
			_, _ = s.AddTransaction(ctx, AddTransactionInput{
				BudgetID:        input.BudgetID,
				UserID:          input.UserID,
				CorrelationID:   corrID,
				TxType:          TxExpense,
				SourceAccountID: acct,
				AmountMinor:     amt,
				Currency:        "PLN",
				BucketID:        &foodBucket,
				Notes:           payee + " Zakupy",
				TransactionDate: currDate,
			})
		}

		// Dining out / cafe
		if rng.Float32() < 0.25 {
			cafeBucket := bucketMap["Restauracje i wyjścia"]
			payee := "Green Caffe Nero"
			if rng.Float32() < 0.5 {
				payee = "Restauracja Sphinx"
			}
			amt := int64(2500 + rng.Intn(9000))
			_, _ = s.AddTransaction(ctx, AddTransactionInput{
				BudgetID:        input.BudgetID,
				UserID:          input.UserID,
				CorrelationID:   corrID,
				TxType:          TxExpense,
				SourceAccountID: mainAcctID,
				AmountMinor:     amt,
				Currency:        "PLN",
				BucketID:        &cafeBucket,
				Notes:           payee,
				TransactionDate: currDate,
			})
		}

		// Fuel / Transport (Orlen) once every 10-14 days
		if rng.Float32() < 0.08 {
			fuelBucket := bucketMap["Paliwo i transport"]
			amt := int64(18000 + rng.Intn(14000))
			_, _ = s.AddTransaction(ctx, AddTransactionInput{
				BudgetID:        input.BudgetID,
				UserID:          input.UserID,
				CorrelationID:   corrID,
				TxType:          TxExpense,
				SourceAccountID: mainAcctID,
				AmountMinor:     amt,
				Currency:        "PLN",
				BucketID:        &fuelBucket,
				Notes:           "PKN ORLEN Stacja Paliw",
				TransactionDate: currDate,
			})
		}

		// Advance day
		currDate = currDate.AddDate(0, 0, 1)
		if rep != nil {
			rep.Advance(1)
		}
	}

	if rep != nil {
		rep.CompleteStep(fmt.Sprintf("Populated realistic multi-currency transactions across %d simulated days", daysCount))
	}

	return nil
}
