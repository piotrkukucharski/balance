Feature: Diagnostic and Audit Tools for Budget Owner
  As a budget Owner
  I want to access a dedicated debug subpage to inspect budget integrity
  So that I can detect anomalies, view orphaned balances, and execute corrective repairs asynchronously

  Scenario: Cannot delete an account with non-starting transactions
    Given an open budget "Family Home"
    And an active checking account "Active Checking" in current budget
    And a budget transaction exists for account "Active Checking"
    When I attempt to delete account "Active Checking"
    Then the delete account request should be rejected with bad request status

  Scenario: Successfully deleting an account with only initial Set Balance transaction
    Given an open budget "Family Home"
    And an account "Empty Bank" with only initial Set Balance offset transaction and zero balance
    When I delete account "Empty Bank"
    Then the account "Empty Bank" should be removed from "accounts_current"
    And the initial Set Balance transaction should be removed from "budget_transactions_current"
    And the currency balance in "account_currency_balances" for "Empty Bank" should be cleaned up

  Scenario: Owner accesses the debug dashboard
    Given an authenticated user "owner@example.com" with "OWNER" role on budget "Integrity Test"
    When I request the debug view "/budgets/:budgetId/debug"
    Then the response status should be 200 OK
    And the page should contain "Diagnostic Tools (Debug)"
    And the sidebar should include a link to the debug page

  Scenario: Non-owner member is forbidden from accessing the debug dashboard
    Given an authenticated user "editor@example.com" with "EDITOR" role on budget "Integrity Test"
    When I request the debug view "/budgets/:budgetId/debug"
    Then the response status should be 403 Forbidden

  Scenario: Asynchronous integrity audit detects orphaned currency balances
    Given an authenticated user "owner@example.com" with "OWNER" role on budget "Integrity Test"
    And an orphaned balance exists in "account_currency_balances" without corresponding account
    When I trigger the integrity audit check via HTMX
    Then the audit result should report an anomaly for orphaned account currency balances

  Scenario: Asynchronous corrective action cleans up orphaned balances in a single transaction
    Given an authenticated user "owner@example.com" with "OWNER" role on budget "Integrity Test"
    And an orphaned balance exists in "account_currency_balances" without corresponding account
    When I trigger the cleanup orphaned balances action via HTMX
    Then the orphaned balance should be deleted from "account_currency_balances"
    And the response should include a success confirmation notification

