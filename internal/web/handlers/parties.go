package handlers

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"html/template"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/balance/balance/internal/ai"
	"github.com/balance/balance/internal/db"
	"github.com/balance/balance/internal/domain"
	"github.com/balance/balance/internal/rule"
	"github.com/balance/balance/internal/web/templates"
	"github.com/labstack/echo/v4"
)

type PartiesHandler struct {
	mainDB          *sql.DB
	tenantMgr       *db.TenantDBManager
	smartRuleAIProv ai.SmartRuleAIProvider
}

func NewPartiesHandler(mainDB *sql.DB, tenantMgr *db.TenantDBManager) *PartiesHandler {
	return &PartiesHandler{
		mainDB:          mainDB,
		tenantMgr:       tenantMgr,
		smartRuleAIProv: ai.NewSmartRuleAIProvider(),
	}
}

func (h *PartiesHandler) SetSmartRuleAIProvider(prov ai.SmartRuleAIProvider) {
	h.smartRuleAIProv = prov
}

type partyAccountInfo struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	Type     string `json:"type"`
	Currency string `json:"currency"`
	IBAN     string `json:"iban,omitempty"`
}

type partyListItem struct {
	ID          string             `json:"id"`
	Name        string             `json:"name"`
	NameJS      string             `json:"-"`
	PartyType   string             `json:"party_type"`
	IsUserOwned int                `json:"is_user_owned"`
	Accounts    []partyAccountInfo `json:"accounts"`
}

type partiesViewData struct {
	Locale                  string
	NumberFormat            string
	BudgetID                string
	BudgetName              string
	CSRFToken               string
	DateFormat              string
	TimeFormat              string
	Parties                 []partyListItem
	SidebarHTML             template.HTML
	BottomDockHTML          template.HTML
	DateTimeFormatterScript template.HTML
}

// PartiesView renders external counterparties, matching rules and counterparty cashflow.
func (h *PartiesHandler) PartiesView(c echo.Context) error {
	budgetID := c.Param("budgetId")
	tenantDB := c.Get(ContextBudgetDBKey).(*sql.DB)

	// Fetch all parties
	pRows, err := tenantDB.QueryContext(c.Request().Context(), `
		SELECT id, name, party_type, is_user_owned
		FROM parties_current
		ORDER BY is_user_owned DESC, name ASC`)
	if err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, "Failed loading parties")
	}
	defer pRows.Close()

	var parties []partyListItem
	partyMap := make(map[string]int)
	for pRows.Next() {
		var it partyListItem
		pRows.Scan(&it.ID, &it.Name, &it.PartyType, &it.IsUserOwned)
		it.NameJS = strings.ReplaceAll(strings.ReplaceAll(it.Name, "\\", "\\\\"), "'", "\\'")
		it.Accounts = make([]partyAccountInfo, 0)
		partyMap[it.ID] = len(parties)
		parties = append(parties, it)
	}

	// Fetch all accounts belonging to parties
	aRows, err := tenantDB.QueryContext(c.Request().Context(), `
		SELECT id, party_id, name, account_type, primary_currency, COALESCE(iban, '')
		FROM accounts_current
		ORDER BY name ASC`)
	if err == nil {
		defer aRows.Close()
		for aRows.Next() {
			var aID, pID, aName, aType, aCurr, aIBAN string
			aRows.Scan(&aID, &pID, &aName, &aType, &aCurr, &aIBAN)
			if idx, ok := partyMap[pID]; ok {
				parties[idx].Accounts = append(parties[idx].Accounts, partyAccountInfo{
					ID:       aID,
					Name:     aName,
					Type:     aType,
					Currency: aCurr,
					IBAN:     aIBAN,
				})
			}
		}
	}

	csrfToken, _ := c.Get("csrfToken").(string)
	userLocale := GetUserLocale(c)
	userNumberFormat := GetUserNumberFormat(c)
	dFormat, tFormat := GetUserDateTimePreferences(c)

	out, err := templates.RenderString("parties/view", partiesViewData{
		Locale:                  userLocale,
		NumberFormat:            userNumberFormat,
		BudgetID:                budgetID,
		BudgetName:              "Parties",
		CSRFToken:               csrfToken,
		DateFormat:              dFormat,
		TimeFormat:              tFormat,
		Parties:                 parties,
		SidebarHTML:             template.HTML(RenderSidebarNavigation(budgetID, "parties", userLocale)),
		BottomDockHTML:          template.HTML(RenderBottomDock(budgetID, "parties", userLocale)),
		DateTimeFormatterScript: template.HTML(DateTimeFormatterScript()),
	})
	if err != nil {
		return err
	}
	return c.HTML(http.StatusOK, out)
}

// RenameParty renames an existing party.
func (h *PartiesHandler) RenameParty(c echo.Context) error {
	budgetID := c.Param("budgetId")
	partyID := c.Param("partyId")
	tenantDB := c.Get(ContextBudgetDBKey).(*sql.DB)
	userID := c.Get(ContextUserIDKey).(string)
	corrID := c.Get("correlationID").(string)

	name := strings.TrimSpace(c.FormValue("name"))
	if name == "" {
		return echo.NewHTTPError(http.StatusBadRequest, "Name is required")
	}

	var curVer int
	var curType string
	var curOwned int
	var curNotes sql.NullString
	err := tenantDB.QueryRowContext(c.Request().Context(), `
		SELECT version, party_type, is_user_owned, notes
		FROM parties_current WHERE id = ?`, partyID).Scan(&curVer, &curType, &curOwned, &curNotes)
	if err != nil {
		return echo.NewHTTPError(http.StatusNotFound, "Party not found")
	}

	now := time.Now().UTC().UnixMicro()
	nextVer := curVer + 1

	tx, err := tenantDB.BeginTx(c.Request().Context(), nil)
	if err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, "DB error")
	}
	defer tx.Rollback()

	_, err = tx.ExecContext(c.Request().Context(), `
		INSERT INTO parties (id, version, name, party_type, is_user_owned, notes, action, agent_id, correlation_id, created_at)
		VALUES (?, ?, ?, ?, ?, ?, 'UPDATE', ?, ?, ?)`,
		partyID, nextVer, name, curType, curOwned, curNotes.String, userID, corrID, now,
	)
	if err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, "Failed inserting party event")
	}

	_, err = tx.ExecContext(c.Request().Context(), `
		UPDATE parties_current
		SET version = ?, name = ?, agent_id = ?, updated_at = ?
		WHERE id = ?`,
		nextVer, name, userID, now, partyID,
	)
	if err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, "Failed updating party projection")
	}

	if err := tx.Commit(); err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, "Commit error")
	}

	return c.Redirect(http.StatusSeeOther, fmt.Sprintf("/budgets/%s/parties", budgetID))
}

// MergeParties merges multiple parties into one target party.
func (h *PartiesHandler) MergeParties(c echo.Context) error {
	budgetID := c.Param("budgetId")
	tenantDB := c.Get(ContextBudgetDBKey).(*sql.DB)
	userID := c.Get(ContextUserIDKey).(string)
	corrID := c.Get("correlationID").(string)

	targetPartyID := strings.TrimSpace(c.FormValue("target_party_id"))
	targetName := strings.TrimSpace(c.FormValue("target_name"))
	sourcePartyIDs := c.Request().Form["source_party_ids"]

	if targetPartyID == "" || len(sourcePartyIDs) < 2 {
		return echo.NewHTTPError(http.StatusBadRequest, "Must select at least two parties to merge")
	}

	// Verify target party exists
	var curVer int
	var curType string
	var curOwned int
	var curNotes sql.NullString
	err := tenantDB.QueryRowContext(c.Request().Context(), `
		SELECT version, party_type, is_user_owned, notes
		FROM parties_current WHERE id = ?`, targetPartyID).Scan(&curVer, &curType, &curOwned, &curNotes)
	if err != nil {
		return echo.NewHTTPError(http.StatusNotFound, "Target party not found")
	}

	if targetName == "" {
		targetName = "Merged Party"
	}

	now := time.Now().UTC().UnixMicro()

	tx, err := tenantDB.BeginTx(c.Request().Context(), nil)
	if err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, "DB error")
	}
	defer tx.Rollback()

	// 1. Update target party name and version
	nextVer := curVer + 1
	_, err = tx.ExecContext(c.Request().Context(), `
		INSERT INTO parties (id, version, name, party_type, is_user_owned, notes, action, agent_id, correlation_id, created_at)
		VALUES (?, ?, ?, ?, ?, ?, 'UPDATE', ?, ?, ?)`,
		targetPartyID, nextVer, targetName, curType, curOwned, curNotes.String, userID, corrID, now,
	)
	if err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, "Failed updating target party event")
	}
	_, err = tx.ExecContext(c.Request().Context(), `
		UPDATE parties_current
		SET version = ?, name = ?, agent_id = ?, updated_at = ?
		WHERE id = ?`,
		nextVer, targetName, userID, now, targetPartyID,
	)
	if err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, "Failed updating target party current")
	}

	// Find target primary account for matching rules
	var targetAccountID string
	_ = tx.QueryRowContext(c.Request().Context(), `SELECT id FROM accounts_current WHERE party_id = ? LIMIT 1`, targetPartyID).Scan(&targetAccountID)

	// 2. For each other source party: move accounts and matching rules, then SOFT_DELETE the party
	for _, srcID := range sourcePartyIDs {
		if srcID == targetPartyID {
			continue
		}

		var srcVer int
		var srcName, sType string
		var sOwned int
		var sNotes sql.NullString
		sErr := tx.QueryRowContext(c.Request().Context(), `
			SELECT version, name, party_type, is_user_owned, notes
			FROM parties_current WHERE id = ?`, srcID).Scan(&srcVer, &srcName, &sType, &sOwned, &sNotes)
		if sErr != nil {
			continue
		}

		// Re-assign accounts
		_, err = tx.ExecContext(c.Request().Context(), `
			UPDATE accounts_current SET party_id = ?, updated_at = ? WHERE party_id = ?`,
			targetPartyID, now, srcID,
		)
		if err != nil {
			return echo.NewHTTPError(http.StatusInternalServerError, "Failed transferring accounts")
		}

		// Re-assign matching rules if target account exists
		if targetAccountID != "" {
			_, err = tx.ExecContext(c.Request().Context(), `
				UPDATE matching_rules_current SET target_counterparty_account_id = ?, updated_at = ?
				WHERE target_counterparty_account_id IN (SELECT id FROM accounts_current WHERE party_id = ?)`,
				targetAccountID, now, srcID,
			)
			if err != nil {
				return echo.NewHTTPError(http.StatusInternalServerError, "Failed transferring matching rules")
			}
		}

		// Soft delete source party
		sNextVer := srcVer + 1
		_, err = tx.ExecContext(c.Request().Context(), `
			INSERT INTO parties (id, version, name, party_type, is_user_owned, notes, action, agent_id, correlation_id, created_at)
			VALUES (?, ?, ?, ?, ?, ?, 'SOFT_DELETE', ?, ?, ?)`,
			srcID, sNextVer, srcName, sType, sOwned, sNotes.String, userID, corrID, now,
		)
		if err != nil {
			return echo.NewHTTPError(http.StatusInternalServerError, "Failed soft-deleting merged party event")
		}

		_, err = tx.ExecContext(c.Request().Context(), `DELETE FROM parties_current WHERE id = ?`, srcID)
		if err != nil {
			return echo.NewHTTPError(http.StatusInternalServerError, "Failed removing merged party from projection")
		}
	}

	if err := tx.Commit(); err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, "Commit error")
	}

	return c.Redirect(http.StatusSeeOther, fmt.Sprintf("/budgets/%s/parties", budgetID))
}

// CreateParty creates a new party and its virtual external account.
func (h *PartiesHandler) CreateParty(c echo.Context) error {
	budgetID := c.Param("budgetId")
	tenantDB := c.Get(ContextBudgetDBKey).(*sql.DB)
	userID := c.Get(ContextUserIDKey).(string)
	corrID := c.Get("correlationID").(string)

	name := c.FormValue("name")
	partyType := c.FormValue("party_type")
	if name == "" {
		return echo.NewHTTPError(http.StatusBadRequest, "Name is required")
	}
	if partyType == "" {
		partyType = "ORGANIZATION"
	}

	now := time.Now().UTC().UnixMicro()
	partyID := domain.NewUUIDv7()
	acctID := domain.NewUUIDv7()

	tx, err := tenantDB.BeginTx(c.Request().Context(), nil)
	if err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, "DB error")
	}
	defer tx.Rollback()

	// Append party
	_, err = tx.ExecContext(c.Request().Context(), `
		INSERT INTO parties (id, version, name, party_type, is_user_owned, action, agent_id, correlation_id, created_at)
		VALUES (?, 1, ?, ?, 0, 'CREATE', ?, ?, ?)`,
		partyID, name, partyType, userID, corrID, now,
	)
	if err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, "Failed to insert party")
	}

	// Update parties_current
	_, err = tx.ExecContext(c.Request().Context(), `
		INSERT INTO parties_current (id, version, name, party_type, is_user_owned, agent_id, updated_at)
		VALUES (?, 1, ?, ?, 0, ?, ?)`,
		partyID, name, partyType, userID, now,
	)
	if err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, "Failed to update parties_current")
	}

	// Create virtual external account
	_, err = tx.ExecContext(c.Request().Context(), `
		INSERT INTO accounts_current (id, version, party_id, name, account_type, primary_currency, is_closed, agent_id, updated_at)
		VALUES (?, 1, ?, ?, 'EXTERNAL', 'PLN', 0, ?, ?)`,
		acctID, partyID, name+" POS Account", userID, now,
	)
	if err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, "Failed to create external account")
	}

	if err := tx.Commit(); err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, "Commit error")
	}

	return c.Redirect(http.StatusSeeOther, fmt.Sprintf("/budgets/%s/parties", budgetID))
}

type partyRuleViewData struct {
	ID            string `json:"id"`
	PartyID       string `json:"party_id"`
	PartyName     string `json:"party_name"`
	Name          string `json:"name"`
	Priority      int    `json:"priority"`
	MatchField    string `json:"match_field"`
	MatchOperator string `json:"match_operator"`
	MatchPattern  string `json:"match_pattern"`
	BucketID      string `json:"bucket_id"`
	BucketName    string `json:"bucket_name"`
	RuleJSON      string `json:"-"`
}

type ruleSelectOption struct {
	ID   string
	Name string
}

type allRulesPageViewData struct {
	Locale                  string
	NumberFormat            string
	BudgetID                string
	BudgetName              string
	CSRFToken               string
	DateFormat              string
	TimeFormat              string
	Rules                   []partyRuleViewData
	BucketOptions           []ruleSelectOption
	PartyOptions            []ruleSelectOption
	SidebarHTML             template.HTML
	BottomDockHTML          template.HTML
	DateTimeFormatterScript template.HTML
}

type partyRulesPageViewData struct {
	Locale                  string
	NumberFormat            string
	BudgetID                string
	BudgetName              string
	PartyID                 string
	PartyName               string
	CSRFToken               string
	DateFormat              string
	TimeFormat              string
	Rules                   []partyRuleViewData
	BucketOptions           []ruleSelectOption
	SidebarHTML             template.HTML
	BottomDockHTML          template.HTML
	DateTimeFormatterScript template.HTML
}

// AllRulesView renders the global rules page at /budgets/{budgetId}/parties/rules.
func (h *PartiesHandler) AllRulesView(c echo.Context) error {
	budgetID := c.Param("budgetId")
	tenantDB := c.Get(ContextBudgetDBKey).(*sql.DB)

	// Fetch all rules with party and bucket information
	rows, err := tenantDB.QueryContext(c.Request().Context(), `
		SELECT mr.id, p.id, p.name, mr.name, mr.priority, mr.match_field, mr.match_operator, mr.match_pattern,
		       COALESCE(mr.default_bucket_id, ''), COALESCE(bp.name, '')
		FROM matching_rules_current mr
		JOIN accounts_current a ON mr.target_counterparty_account_id = a.id
		JOIN parties_current p ON a.party_id = p.id
		LEFT JOIN buckets_projection bp ON mr.default_bucket_id = bp.id
		ORDER BY mr.priority ASC, mr.name ASC`)
	if err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, "Failed loading rules: "+err.Error())
	}
	defer rows.Close()

	var rules []partyRuleViewData
	for rows.Next() {
		var r partyRuleViewData
		if err := rows.Scan(&r.ID, &r.PartyID, &r.PartyName, &r.Name, &r.Priority, &r.MatchField, &r.MatchOperator, &r.MatchPattern, &r.BucketID, &r.BucketName); err == nil {
			rJSON, _ := json.Marshal(r)
			r.RuleJSON = string(rJSON)
			rules = append(rules, r)
		}
	}

	// Fetch buckets
	bRows, err := tenantDB.QueryContext(c.Request().Context(), `
		SELECT id, name FROM buckets_projection WHERE is_archived = 0 ORDER BY name ASC`)
	var bucketOptions []ruleSelectOption
	if err == nil {
		defer bRows.Close()
		for bRows.Next() {
			var bID, bName string
			bRows.Scan(&bID, &bName)
			bucketOptions = append(bucketOptions, ruleSelectOption{ID: bID, Name: bName})
		}
	}

	// Fetch parties for rule assignment
	partyRows, err := tenantDB.QueryContext(c.Request().Context(), `
		SELECT id, name FROM parties_current ORDER BY name ASC`)
	var partyOptions []ruleSelectOption
	if err == nil {
		defer partyRows.Close()
		for partyRows.Next() {
			var pID, pName string
			partyRows.Scan(&pID, &pName)
			partyOptions = append(partyOptions, ruleSelectOption{ID: pID, Name: pName})
		}
	}

	csrfToken, _ := c.Get("csrfToken").(string)
	userLocale := GetUserLocale(c)
	userNumberFormat := GetUserNumberFormat(c)
	dFormat, tFormat := GetUserDateTimePreferences(c)

	out, err := templates.RenderString("parties/all_rules", allRulesPageViewData{
		Locale:                  userLocale,
		NumberFormat:            userNumberFormat,
		BudgetID:                budgetID,
		BudgetName:              "Rules",
		CSRFToken:               csrfToken,
		DateFormat:              dFormat,
		TimeFormat:              tFormat,
		Rules:                   rules,
		BucketOptions:           bucketOptions,
		PartyOptions:            partyOptions,
		SidebarHTML:             template.HTML(RenderSidebarNavigation(budgetID, "parties", userLocale)),
		BottomDockHTML:          template.HTML(RenderBottomDock(budgetID, "parties", userLocale)),
		DateTimeFormatterScript: template.HTML(DateTimeFormatterScript()),
	})
	if err != nil {
		return err
	}
	return c.HTML(http.StatusOK, out)
}

// PartyRulesView renders the dedicated page with matching rules for a specific party.
func (h *PartiesHandler) PartyRulesView(c echo.Context) error {
	budgetID := c.Param("budgetId")
	partyID := c.Param("partyId")
	tenantDB := c.Get(ContextBudgetDBKey).(*sql.DB)

	// Verify party exists
	var partyName, partyType string
	var isUserOwned int
	err := tenantDB.QueryRowContext(c.Request().Context(), `
		SELECT name, party_type, is_user_owned
		FROM parties_current
		WHERE id = ?`, partyID).Scan(&partyName, &partyType, &isUserOwned)
	if err != nil {
		if err == sql.ErrNoRows {
			return echo.NewHTTPError(http.StatusNotFound, "Party not found")
		}
		return echo.NewHTTPError(http.StatusInternalServerError, "Failed loading party: "+err.Error())
	}

	// Fetch rules for this party
	rows, err := tenantDB.QueryContext(c.Request().Context(), `
		SELECT mr.id, mr.name, mr.priority, mr.match_field, mr.match_operator, mr.match_pattern,
		       COALESCE(mr.default_bucket_id, ''), COALESCE(bp.name, '')
		FROM matching_rules_current mr
		JOIN accounts_current a ON mr.target_counterparty_account_id = a.id
		LEFT JOIN buckets_projection bp ON mr.default_bucket_id = bp.id
		WHERE a.party_id = ?
		ORDER BY mr.priority ASC, mr.name ASC`, partyID)
	if err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, "Failed loading matching rules: "+err.Error())
	}
	defer rows.Close()

	var rules []partyRuleViewData
	for rows.Next() {
		var r partyRuleViewData
		r.PartyID = partyID
		r.PartyName = partyName
		if err := rows.Scan(&r.ID, &r.Name, &r.Priority, &r.MatchField, &r.MatchOperator, &r.MatchPattern, &r.BucketID, &r.BucketName); err == nil {
			rJSON, _ := json.Marshal(r)
			r.RuleJSON = string(rJSON)
			rules = append(rules, r)
		}
	}

	// Fetch buckets for dropdown
	bRows, err := tenantDB.QueryContext(c.Request().Context(), `
		SELECT id, name
		FROM buckets_projection
		WHERE is_archived = 0
		ORDER BY name ASC`)
	var bucketOptions []ruleSelectOption
	if err == nil {
		defer bRows.Close()
		for bRows.Next() {
			var bID, bName string
			if err := bRows.Scan(&bID, &bName); err == nil {
				bucketOptions = append(bucketOptions, ruleSelectOption{ID: bID, Name: bName})
			}
		}
	}

	csrfToken, _ := c.Get("csrfToken").(string)
	userLocale := GetUserLocale(c)
	userNumberFormat := GetUserNumberFormat(c)
	dFormat, tFormat := GetUserDateTimePreferences(c)

	out, err := templates.RenderString("parties/party_rules", partyRulesPageViewData{
		Locale:                  userLocale,
		NumberFormat:            userNumberFormat,
		BudgetID:                budgetID,
		BudgetName:              "Parties",
		PartyID:                 partyID,
		PartyName:               partyName,
		CSRFToken:               csrfToken,
		DateFormat:              dFormat,
		TimeFormat:              tFormat,
		Rules:                   rules,
		BucketOptions:           bucketOptions,
		SidebarHTML:             template.HTML(RenderSidebarNavigation(budgetID, "parties", userLocale)),
		BottomDockHTML:          template.HTML(RenderBottomDock(budgetID, "parties", userLocale)),
		DateTimeFormatterScript: template.HTML(DateTimeFormatterScript()),
	})
	if err != nil {
		return err
	}
	return c.HTML(http.StatusOK, out)
}

// CreatePartyRule creates an auto-matching rule for a party.
func (h *PartiesHandler) CreatePartyRule(c echo.Context) error {
	budgetID := c.Param("budgetId")
	partyID := c.Param("partyId")
	tenantDB := c.Get(ContextBudgetDBKey).(*sql.DB)
	userID := c.Get(ContextUserIDKey).(string)
	corrID := c.Get("correlationID").(string)

	name := strings.TrimSpace(c.FormValue("name"))
	matchField := strings.TrimSpace(c.FormValue("match_field"))
	matchOperator := strings.TrimSpace(c.FormValue("match_operator"))
	matchPattern := strings.TrimSpace(c.FormValue("match_pattern"))
	priorityStr := strings.TrimSpace(c.FormValue("priority"))
	defaultBucketID := strings.TrimSpace(c.FormValue("default_bucket_id"))
	redirectURL := c.QueryParam("redirect")
	if redirectURL == "" {
		redirectURL = c.FormValue("redirect")
	}

	if name == "" || matchPattern == "" {
		return echo.NewHTTPError(http.StatusBadRequest, "Name and Pattern are required")
	}

	validFields := map[string]bool{
		"RAW_PAYEE":         true,
		"COUNTERPARTY_IBAN": true,
		"REFERENCE":         true,
		"ANY":               true,
	}
	if !validFields[matchField] {
		matchField = "RAW_PAYEE"
	}

	validOps := map[string]bool{
		"CONTAINS": true,
		"PREFIX":   true,
		"EXACT":    true,
		"REGEX":    true,
	}
	if !validOps[matchOperator] {
		matchOperator = "CONTAINS"
	}

	if matchOperator == "REGEX" {
		if _, err := regexp.Compile(matchPattern); err != nil {
			return echo.NewHTTPError(http.StatusBadRequest, "Invalid regular expression pattern: "+err.Error())
		}
	}

	priority := 100
	if p, err := strconv.Atoi(priorityStr); err == nil && p > 0 {
		priority = p
	}

	// Locate counterparty's account ID
	var targetAccountID string
	err := tenantDB.QueryRowContext(c.Request().Context(), `
		SELECT id FROM accounts_current WHERE party_id = ? LIMIT 1`, partyID).Scan(&targetAccountID)
	if err != nil {
		if err == sql.ErrNoRows {
			// Auto create external account if none exists
			var partyName string
			_ = tenantDB.QueryRowContext(c.Request().Context(), `SELECT name FROM parties_current WHERE id = ?`, partyID).Scan(&partyName)
			targetAccountID = domain.NewUUIDv7()
			now := time.Now().UTC().UnixMicro()
			_, _ = tenantDB.ExecContext(c.Request().Context(), `
				INSERT INTO accounts_current (id, version, party_id, name, account_type, primary_currency, is_closed, agent_id, updated_at)
				VALUES (?, 1, ?, ?, 'EXTERNAL', 'PLN', 0, ?, ?)`,
				targetAccountID, partyID, partyName+" Account", userID, now,
			)
		} else {
			return echo.NewHTTPError(http.StatusInternalServerError, "Failed locating party account: "+err.Error())
		}
	}

	var bucketIDPtr *string
	if defaultBucketID != "" {
		bucketIDPtr = &defaultBucketID
	}

	ruleID := domain.NewUUIDv7()
	now := time.Now().UTC().UnixMicro()

	tx, err := tenantDB.BeginTx(c.Request().Context(), nil)
	if err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, "Transaction error: "+err.Error())
	}
	defer tx.Rollback()

	_, err = tx.ExecContext(c.Request().Context(), `
		INSERT INTO matching_rules (
			id, version, name, priority, match_field, match_operator, match_pattern,
			target_counterparty_account_id, default_bucket_id, action, agent_id, correlation_id, created_at
		) VALUES (?, 1, ?, ?, ?, ?, ?, ?, ?, 'CREATE', ?, ?, ?)`,
		ruleID, name, priority, matchField, matchOperator, matchPattern,
		targetAccountID, bucketIDPtr, userID, corrID, now,
	)
	if err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, "Failed inserting matching rule: "+err.Error())
	}

	_, err = tx.ExecContext(c.Request().Context(), `
		INSERT INTO matching_rules_current (
			id, version, name, priority, match_field, match_operator, match_pattern,
			target_counterparty_account_id, default_bucket_id, updated_at
		) VALUES (?, 1, ?, ?, ?, ?, ?, ?, ?, ?)`,
		ruleID, name, priority, matchField, matchOperator, matchPattern,
		targetAccountID, bucketIDPtr, now,
	)
	if err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, "Failed updating matching rules projection: "+err.Error())
	}

	if err := tx.Commit(); err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, "Commit error: "+err.Error())
	}

	if redirectURL != "" {
		return c.Redirect(http.StatusSeeOther, redirectURL)
	}
	return c.Redirect(http.StatusSeeOther, fmt.Sprintf("/budgets/%s/parties/%s/rules", budgetID, partyID))
}

// UpdatePartyRule updates an existing matching rule.
func (h *PartiesHandler) UpdatePartyRule(c echo.Context) error {
	budgetID := c.Param("budgetId")
	partyID := c.Param("partyId")
	ruleID := c.Param("ruleId")
	tenantDB := c.Get(ContextBudgetDBKey).(*sql.DB)
	userID := c.Get(ContextUserIDKey).(string)
	corrID := c.Get("correlationID").(string)

	name := strings.TrimSpace(c.FormValue("name"))
	matchField := strings.TrimSpace(c.FormValue("match_field"))
	matchOperator := strings.TrimSpace(c.FormValue("match_operator"))
	matchPattern := strings.TrimSpace(c.FormValue("match_pattern"))
	priorityStr := strings.TrimSpace(c.FormValue("priority"))
	defaultBucketID := strings.TrimSpace(c.FormValue("default_bucket_id"))
	redirectURL := c.QueryParam("redirect")
	if redirectURL == "" {
		redirectURL = c.FormValue("redirect")
	}

	if name == "" || matchPattern == "" {
		return echo.NewHTTPError(http.StatusBadRequest, "Name and Pattern are required")
	}

	if matchOperator == "REGEX" {
		if _, err := regexp.Compile(matchPattern); err != nil {
			return echo.NewHTTPError(http.StatusBadRequest, "Invalid regular expression pattern: "+err.Error())
		}
	}

	priority := 100
	if p, err := strconv.Atoi(priorityStr); err == nil && p > 0 {
		priority = p
	}

	var bucketIDPtr *string
	if defaultBucketID != "" {
		bucketIDPtr = &defaultBucketID
	}

	var curVer int
	var tgtAcctID string
	err := tenantDB.QueryRowContext(c.Request().Context(), `
		SELECT version, target_counterparty_account_id
		FROM matching_rules_current WHERE id = ?`, ruleID).Scan(&curVer, &tgtAcctID)
	if err != nil {
		return echo.NewHTTPError(http.StatusNotFound, "Matching rule not found")
	}

	now := time.Now().UTC().UnixMicro()
	nextVer := curVer + 1

	tx, err := tenantDB.BeginTx(c.Request().Context(), nil)
	if err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, "DB error")
	}
	defer tx.Rollback()

	_, err = tx.ExecContext(c.Request().Context(), `
		INSERT INTO matching_rules (
			id, version, name, priority, match_field, match_operator, match_pattern,
			target_counterparty_account_id, default_bucket_id, action, agent_id, correlation_id, created_at
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, 'UPDATE', ?, ?, ?)`,
		ruleID, nextVer, name, priority, matchField, matchOperator, matchPattern,
		tgtAcctID, bucketIDPtr, userID, corrID, now,
	)
	if err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, "Failed inserting matching rule event")
	}

	_, err = tx.ExecContext(c.Request().Context(), `
		UPDATE matching_rules_current
		SET version = ?, name = ?, priority = ?, match_field = ?, match_operator = ?, match_pattern = ?, default_bucket_id = ?, updated_at = ?
		WHERE id = ?`,
		nextVer, name, priority, matchField, matchOperator, matchPattern, bucketIDPtr, now, ruleID,
	)
	if err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, "Failed updating matching rules projection")
	}

	if err := tx.Commit(); err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, "Commit error")
	}

	if redirectURL != "" {
		return c.Redirect(http.StatusSeeOther, redirectURL)
	}
	return c.Redirect(http.StatusSeeOther, fmt.Sprintf("/budgets/%s/parties/%s/rules", budgetID, partyID))
}

// DeletePartyRule deletes a matching rule.
func (h *PartiesHandler) DeletePartyRule(c echo.Context) error {
	budgetID := c.Param("budgetId")
	partyID := c.Param("partyId")
	ruleID := c.Param("ruleId")
	tenantDB := c.Get(ContextBudgetDBKey).(*sql.DB)
	redirectURL := c.QueryParam("redirect")

	tx, err := tenantDB.BeginTx(c.Request().Context(), nil)
	if err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, "Transaction error: "+err.Error())
	}
	defer tx.Rollback()

	_, err = tx.ExecContext(c.Request().Context(), `DELETE FROM matching_rules_current WHERE id = ?`, ruleID)
	if err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, "Failed deleting from matching_rules_current: "+err.Error())
	}

	_, err = tx.ExecContext(c.Request().Context(), `DELETE FROM matching_rules WHERE id = ?`, ruleID)
	if err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, "Failed deleting from matching_rules: "+err.Error())
	}

	if err := tx.Commit(); err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, "Commit error: "+err.Error())
	}

	if redirectURL != "" {
		return c.Redirect(http.StatusSeeOther, redirectURL)
	}
	return c.Redirect(http.StatusSeeOther, fmt.Sprintf("/budgets/%s/parties/%s/rules", budgetID, partyID))
}

// SmartRuleSuggestResponse is returned by SmartSuggestRules.
type SmartRuleSuggestResponse struct {
	UnassignedCount int                     `json:"unassigned_count"`
	ProposedRules   []ai.SmartRuleCandidate `json:"proposed_rules"`
}

// SmartSuggestRules inspects unassigned raw transactions and suggests AI rules.
func (h *PartiesHandler) SmartSuggestRules(c echo.Context) error {
	tenantDB := c.Get(ContextBudgetDBKey).(*sql.DB)

	// 1. Compile active matching rules engine
	rulesEngine, err := rule.LoadRulesFromDB(c.Request().Context(), tenantDB)
	if err != nil {
		rulesEngine, _ = rule.NewMatchingEngine([]*rule.MatchingRule{})
	}

	// 2. Fetch pending transactions
	rows, err := tenantDB.QueryContext(c.Request().Context(), `
		SELECT id, raw_payee, COALESCE(counterparty_iban, ''), COALESCE(reference, ''), currency, amount_minor
		FROM raw_transactions_current
		WHERE status = 'PENDING'
		ORDER BY booking_date DESC LIMIT 200`)
	if err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, "Failed loading transactions: "+err.Error())
	}
	defer rows.Close()

	var unassignedTxs []ai.UnassignedTxInfo
	for rows.Next() {
		var id, payee, iban, ref, curr string
		var amountMinor int64
		if err := rows.Scan(&id, &payee, &iban, &ref, &curr, &amountMinor); err == nil {
			// Check if already matched
			if match := rulesEngine.Match(payee, iban, ref); match == nil {
				unassignedTxs = append(unassignedTxs, ai.UnassignedTxInfo{
					ID:        id,
					RawPayee:  payee,
					IBAN:      iban,
					Reference: ref,
					Currency:  curr,
					Amount:    fmt.Sprintf("%.2f", float64(amountMinor)/100.0),
				})
			}
		}
	}

	// 3. Available buckets
	var buckets []string
	bRows, err := tenantDB.QueryContext(c.Request().Context(), `SELECT name FROM buckets_projection WHERE is_archived = 0 ORDER BY name ASC`)
	if err == nil {
		defer bRows.Close()
		for bRows.Next() {
			var bName string
			bRows.Scan(&bName)
			buckets = append(buckets, bName)
		}
	}

	// 4. Existing parties
	var parties []string
	pRows, err := tenantDB.QueryContext(c.Request().Context(), `SELECT name FROM parties_current ORDER BY name ASC`)
	if err == nil {
		defer pRows.Close()
		for pRows.Next() {
			var pName string
			pRows.Scan(&pName)
			parties = append(parties, pName)
		}
	}

	if len(unassignedTxs) == 0 {
		return c.JSON(http.StatusOK, SmartRuleSuggestResponse{
			UnassignedCount: 0,
			ProposedRules:   []ai.SmartRuleCandidate{},
		})
	}

	proposed, err := h.smartRuleAIProv.SuggestRules(c.Request().Context(), ai.SmartRulePromptRequest{
		Transactions:     unassignedTxs,
		AvailableBuckets: buckets,
		ExistingParties:  parties,
	})
	if err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, "Failed calling AI rule generator: "+err.Error())
	}

	return c.JSON(http.StatusOK, SmartRuleSuggestResponse{
		UnassignedCount: len(unassignedTxs),
		ProposedRules:   proposed,
	})
}

// SmartTestRules tests proposed rules against current unassigned transactions without saving.
func (h *PartiesHandler) SmartTestRules(c echo.Context) error {
	tenantDB := c.Get(ContextBudgetDBKey).(*sql.DB)

	var req struct {
		Rules []ai.SmartRuleCandidate `json:"rules"`
	}
	if err := c.Bind(&req); err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, "Invalid request payload")
	}

	var testRules []*rule.MatchingRule
	for idx, r := range req.Rules {
		testRules = append(testRules, &rule.MatchingRule{
			ID:                       fmt.Sprintf("test-%d", idx),
			Name:                     r.Name,
			Priority:                 100 + idx,
			MatchField:               r.MatchField,
			MatchOperator:            r.MatchOperator,
			MatchPattern:             r.MatchPattern,
			TargetCounterpartyAcctID: "dummy-target-id",
		})
	}

	testEngine, err := rule.NewMatchingEngine(testRules)
	if err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, "Invalid rule pattern in proposed rules: "+err.Error())
	}

	rows, err := tenantDB.QueryContext(c.Request().Context(), `
		SELECT raw_payee, COALESCE(counterparty_iban, ''), COALESCE(reference, '')
		FROM raw_transactions_current
		WHERE status = 'PENDING'`)
	if err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, "Failed querying raw transactions")
	}
	defer rows.Close()

	total := 0
	matched := 0
	for rows.Next() {
		total++
		var payee, iban, ref string
		rows.Scan(&payee, &iban, &ref)
		if testEngine.Match(payee, iban, ref) != nil {
			matched++
		}
	}

	return c.JSON(http.StatusOK, map[string]any{
		"total_unassigned": total,
		"matched_count":    matched,
	})
}

// SmartSaveRules saves the selected AI-proposed rules.
func (h *PartiesHandler) SmartSaveRules(c echo.Context) error {
	budgetID := c.Param("budgetId")
	tenantDB := c.Get(ContextBudgetDBKey).(*sql.DB)
	userID := c.Get(ContextUserIDKey).(string)
	corrID := c.Get("correlationID").(string)

	if err := c.Request().ParseForm(); err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, "Invalid form data")
	}

	formParams, _ := c.FormParams()
	selectedIndices := formParams["selected_rules"]
	if len(selectedIndices) == 0 {
		selectedIndices = c.Request().Form["selected_rules"]
	}
	if len(selectedIndices) == 0 {
		selectedIndices = c.Request().PostForm["selected_rules"]
	}
	if len(selectedIndices) == 0 {
		return c.Redirect(http.StatusSeeOther, fmt.Sprintf("/budgets/%s/parties/rules", budgetID))
	}

	now := time.Now().UTC().UnixMicro()

	tx, err := tenantDB.BeginTx(c.Request().Context(), nil)
	if err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, "DB error")
	}
	defer tx.Rollback()

	getVal := func(key string) string {
		v := c.FormValue(key)
		if v != "" { return v }
		if vals, ok := formParams[key]; ok && len(vals) > 0 { return vals[0] }
		if c.Request().Form != nil && len(c.Request().Form[key]) > 0 { return c.Request().Form[key][0] }
		if c.Request().PostForm != nil && len(c.Request().PostForm[key]) > 0 { return c.Request().PostForm[key][0] }
		return ""
	}

	for _, idx := range selectedIndices {
		name := strings.TrimSpace(getVal("rule_" + idx + "_name"))
		targetPartyName := strings.TrimSpace(getVal("rule_" + idx + "_target_party"))
		pattern := strings.TrimSpace(getVal("rule_" + idx + "_pattern"))
		field := strings.TrimSpace(getVal("rule_" + idx + "_field"))
		operator := strings.TrimSpace(getVal("rule_" + idx + "_operator"))
		bucketName := strings.TrimSpace(getVal("rule_" + idx + "_bucket"))

		if name == "" || pattern == "" || targetPartyName == "" {
			continue
		}
		if field == "" {
			field = "RAW_PAYEE"
		}
		if operator == "" {
			operator = "CONTAINS"
		}

		// Ensure party exists
		var partyID string
		err := tx.QueryRowContext(c.Request().Context(), `SELECT id FROM parties_current WHERE LOWER(name) = LOWER(?) LIMIT 1`, targetPartyName).Scan(&partyID)
		if err == sql.ErrNoRows {
			partyID = domain.NewUUIDv7()
			_, err = tx.ExecContext(c.Request().Context(), `
				INSERT INTO parties (id, version, name, party_type, is_user_owned, action, agent_id, correlation_id, created_at)
				VALUES (?, 1, ?, 'ORGANIZATION', 0, 'CREATE', ?, ?, ?)`,
				partyID, targetPartyName, userID, corrID, now,
			)
			if err != nil {
				return echo.NewHTTPError(http.StatusInternalServerError, "Failed creating party: "+err.Error())
			}
			_, err = tx.ExecContext(c.Request().Context(), `
				INSERT INTO parties_current (id, version, name, party_type, is_user_owned, agent_id, updated_at)
				VALUES (?, 1, ?, 'ORGANIZATION', 0, ?, ?)`,
				partyID, targetPartyName, userID, now,
			)
			if err != nil {
				return echo.NewHTTPError(http.StatusInternalServerError, "Failed updating party projection: "+err.Error())
			}
		}

		// Ensure external account exists
		var accountID string
		err = tx.QueryRowContext(c.Request().Context(), `SELECT id FROM accounts_current WHERE party_id = ? LIMIT 1`, partyID).Scan(&accountID)
		if err == sql.ErrNoRows {
			accountID = domain.NewUUIDv7()
			_, err = tx.ExecContext(c.Request().Context(), `
				INSERT INTO accounts_current (id, version, party_id, name, account_type, primary_currency, is_closed, agent_id, updated_at)
				VALUES (?, 1, ?, ?, 'EXTERNAL', 'PLN', 0, ?, ?)`,
				accountID, partyID, targetPartyName+" Account", userID, now,
			)
			if err != nil {
				return echo.NewHTTPError(http.StatusInternalServerError, "Failed creating account: "+err.Error())
			}
		}

		// Resolve bucket ID
		var bucketIDPtr *string
		if bucketName != "" {
			var bID string
			if err := tx.QueryRowContext(c.Request().Context(), `SELECT id FROM buckets_projection WHERE LOWER(name) = LOWER(?) LIMIT 1`, bucketName).Scan(&bID); err == nil {
				bucketIDPtr = &bID
			}
		}

		ruleID := domain.NewUUIDv7()
		_, err = tx.ExecContext(c.Request().Context(), `
			INSERT INTO matching_rules (
				id, version, name, priority, match_field, match_operator, match_pattern,
				target_counterparty_account_id, default_bucket_id, action, agent_id, correlation_id, created_at
			) VALUES (?, 1, ?, 100, ?, ?, ?, ?, ?, 'CREATE', ?, ?, ?)`,
			ruleID, name, field, operator, pattern, accountID, bucketIDPtr, userID, corrID, now,
		)
		if err != nil {
			return echo.NewHTTPError(http.StatusInternalServerError, "Failed inserting matching rule: "+err.Error())
		}

		_, err = tx.ExecContext(c.Request().Context(), `
			INSERT INTO matching_rules_current (
				id, version, name, priority, match_field, match_operator, match_pattern,
				target_counterparty_account_id, default_bucket_id, updated_at
			) VALUES (?, 1, ?, 100, ?, ?, ?, ?, ?, ?)`,
			ruleID, name, field, operator, pattern, accountID, bucketIDPtr, now,
		)
		if err != nil {
			return echo.NewHTTPError(http.StatusInternalServerError, "Failed inserting matching rule current: "+err.Error())
		}
	}

	if err := tx.Commit(); err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, "Commit error: "+err.Error())
	}

	return c.Redirect(http.StatusSeeOther, fmt.Sprintf("/budgets/%s/parties/rules", budgetID))
}
