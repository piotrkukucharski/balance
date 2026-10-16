Feature: Multi-Tenant Budget Provisioning and Workspace Navigation
  As an authenticated user
  I want to create isolated budget databases and navigate workspace views
  So that my envelopes and ledger are secure and easily manageable

  Scenario: Creating a new isolated tenant budget
    Given an authenticated user "jan@example.com"
    When I create a new budget named "Family Home" with currency "PLN" and timezone "Europe/Warsaw"
    Then a new budget record should be registered in "main.db"
    And the user "jan@example.com" should be assigned the "OWNER" role in "budget_members"
    And an isolated tenant database "budget_{id}.db" should be provisioned with full schema
    And the response should redirect to the budget view "/budgets/:budgetId/budget"

  Scenario: Viewing the YNAB-style budget workspace and navigation
    Given an authenticated user "jan@example.com" who owns the budget "Family Home"
    When I request the budget view "/budgets/:budgetId/budget"
    Then the response status should be 200 OK
    And the view should display the Top Horizon and Ready to Assign pool banner
    And the navigation should contain "Budget", "Accounts", "Transactions", "Reports", "Parties", and "Members"
    And the mobile bottom dock should be rendered for responsive viewports
