package openapi_test

import (
	"testing"

	openapi "github.com/balance/balance/internal/api"
)

func TestOpenAPISpecEmbedded(t *testing.T) {
	swagger, err := openapi.GetSwagger()
	if err != nil {
		t.Fatalf("failed to get embedded swagger spec: %v", err)
	}

	if swagger.Info == nil || swagger.Info.Title == "" {
		t.Fatalf("expected valid Swagger info title, got: %+v", swagger.Info)
	}

	expectedTitle := "Balance Personal Finance REST API"
	if swagger.Info.Title != expectedTitle {
		t.Errorf("expected title %q, got %q", expectedTitle, swagger.Info.Title)
	}

	// Verify core paths exist in contract
	expectedPaths := []string{
		"/auth/register",
		"/auth/login",
		"/auth/logout",
		"/auth/tokens",
		"/auth/tokens/{id}",
		"/users/me",
		"/exchange-rates",
		"/exchange-rates/sync",
		"/budgets",
		"/budgets/{budgetId}",
		"/budgets/{budgetId}/invitations",
		"/invitations/{token}/accept",
		"/integrations/providers",
		"/integrations/{provider}/connect",
		"/integrations/{provider}/callback",
		"/budgets/{budgetId}/integrations/{integrationId}/link-account",
		"/budgets/{budgetId}/integrations/{integrationId}/sync",
		"/budgets/{budgetId}/accounts/{accountId}/import-csv",
		"/budgets/{budgetId}/parties",
		"/budgets/{budgetId}/parties/{id}",
		"/budgets/{budgetId}/parties/{id}/cashflow",
		"/budgets/{budgetId}/accounts",
		"/budgets/{budgetId}/matching-rules",
		"/budgets/{budgetId}/{entityType}/{id}/history",
		"/budgets/{budgetId}/{entityType}/{id}/restore",
		"/budgets/{budgetId}/raw-transactions",
		"/budgets/{budgetId}/transactions",
		"/budgets/{budgetId}/transactions/resolve",
		"/budgets/{budgetId}/buckets",
		"/budgets/{budgetId}/buckets/{id}/targets",
		"/budgets/{budgetId}/targets/generate-formula",
		"/budgets/{budgetId}/targets/validate-formula",
		"/budgets/{budgetId}/targets/{id}/fund",
		"/budgets/{budgetId}/view/horizon",
		"/budgets/{budgetId}/reports/saved",
		"/budgets/{budgetId}/reports/saved/{id}",
		"/budgets/{budgetId}/reports/age-of-money",
		"/budgets/{budgetId}/reports/income-vs-expense",
		"/budgets/{budgetId}/reports/spending-trends",
		"/budgets/{budgetId}/reports/spending-breakdown",
		"/budgets/{budgetId}/reports/net-worth",
		"/budgets/{budgetId}/reports/sankey",
		"/budgets/{budgetId}/reports/balance-forecast",
		"/budgets/{budgetId}/reports/budget-analysis",
		"/budgets/{budgetId}/reports/monte-carlo/simulate",
		"/budgets/{budgetId}/reports/party-cashflow",
	}

	for _, p := range expectedPaths {
		if _, ok := swagger.Paths.Map()[p]; !ok {
			t.Errorf("expected path %q to exist in OpenAPI spec", p)
		}
	}
}
