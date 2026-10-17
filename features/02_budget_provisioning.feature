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
    And the navigation should contain "Budget", "Accounts", "Transactions", "Reports", "Parties", and "Settings"
    And the mobile bottom dock should be rendered for responsive viewports

  Scenario: Navigating and updating budget settings and members subview
    Given an authenticated user "jan@example.com" who owns the budget "Family Home"
    When I request the budget settings view "/budgets/:budgetId/settings"
    Then the response status should be 200 OK
    And the view should display the budget settings form with current name "Family Home"
    And the navigation should contain "Settings" with subelement "Members" linking to "/budgets/:budgetId/settings/members"
    When I submit a request to update the budget name to "Family Home Updated" via "/budgets/:budgetId/settings"
    Then the budget name in "main.db" should be updated to "Family Home Updated"
    And the response should redirect to "/budgets/:budgetId/settings"
    When I request the budget members view "/budgets/:budgetId/settings/members"
    Then the response status should be 200 OK
    And the view should display the members and access control table


  Scenario: Viewing Accounts Summary in budget sidebar for empty budget
    Given an authenticated user "jan@example.com" who owns the budget "Family Home"
    When I request the budget view "/budgets/:budgetId/budget"
    Then the response status should be 200 OK
    And the Accounts Summary should display no connected accounts

  Scenario: Viewing Accounts Summary in budget sidebar with active accounts
    Given an authenticated user "jan@example.com" who owns the budget "Family Home"
    And an active account "Main Checking" with balance "1,200.00 PLN" exists in the budget
    When I request the budget view "/budgets/:budgetId/budget"
    Then the response status should be 200 OK
    And the Accounts Summary should display account "Main Checking" with balance "1,200.00 PLN"

  Scenario: Renaming an existing budget as OWNER
    Given an authenticated user "jan@example.com" who owns the budget "Family Home"
    When I submit a request to rename the budget "Family Home" to "Family Home 2026"
    Then the budget name in "main.db" should be updated to "Family Home 2026"
    And the response should redirect to "/budgets"

  Scenario: Deleting an existing budget as OWNER
    Given an authenticated user "jan@example.com" who owns the budget "Family Home"
    When I submit a request to delete the budget "Family Home"
    Then the budget record should be removed from "main.db"
    And the tenant database file "budget_{id}.db" should be removed from disk
    And the response should redirect to "/budgets"

  Scenario: Non-owner cannot rename or delete a budget
    Given an authenticated user "jan@example.com" who owns the budget "Family Home"
    And an authenticated user "adam@example.com" who is an "ADMIN" in the budget "Family Home"
    When user "adam@example.com" submits a request to rename the budget "Family Home" to "Adam Budget"
    Then the response status should be 403 Forbidden
    When user "adam@example.com" submits a request to delete the budget "Family Home"
    Then the response status should be 403 Forbidden

  Scenario: Budget management actions are visible only to OWNER in UI
    Given an authenticated user "jan@example.com" who owns the budget "Family Home"
    And an authenticated user "adam@example.com" who is an "ADMIN" in the budget "Family Home"
    When user "jan@example.com" requests the budgets list view "/budgets"
    Then the response should contain budget edit and delete controls
    When user "adam@example.com" requests the budgets list view "/budgets"
    Then the response should not contain budget edit and delete controls

