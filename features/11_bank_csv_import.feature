Feature: Bank Statement CSV Import and Reconciliation
  As a budget manager
  I want to upload bank statements in CSV format and preview transaction mappings
  So that I can import historical banking records without Open Banking integrations

  Background:
    Given the Balance application database is initialized
    And an authenticated user "tester@example.com" with an active session
    And a budget named "CSV Testing Budget" exists

  Scenario: Navigating to CSV import page from bank sync modal
    Given an active checking account "Main Checking" in budget "CSV Testing Budget"
    When I request the bank statement CSV import page for budget "CSV Testing Budget"
    Then the response status should be 200 OK
    And the page should contain "Import Bank Statement CSV"
    And the page should list account "Main Checking"

  Scenario: Uploading and previewing CSV statement with auto-detection
    Given an active checking account "Main Checking" in budget "CSV Testing Budget"
    When I submit a CSV preview request for account "Main Checking" with bank profile "GENERIC" and file content:
      """
      Date;Amount;Payee;Reference
      2026-10-01;-120.50;Biedronka;Groceries shopping
      2026-10-02;3500.00;Employer Sp. z o.o.;October Salary
      """
    Then the CSV preview response should contain 2 rows
    And row 1 should have date "2026-10-01", amount "-120.50", and payee "Biedronka"
    And row 2 should have date "2026-10-02", amount "3500.00", and payee "Employer Sp. z o.o."

  Scenario: Importing CSV transactions into raw_transactions_current and running auto-matching
    Given an active checking account "Main Checking" in budget "CSV Testing Budget"
    And an active merchant counterparty "Biedronka" with an auto-matching rule for "Biedronka" to bucket "Groceries"
    When I submit a CSV import request for account "Main Checking" with auto-match enabled and file content:
      """
      Date;Amount;Payee;Reference
      2026-10-01;-120.50;Biedronka;Groceries shopping
      2026-10-02;3500.00;Employer Sp. z o.o.;October Salary
      """
    Then the response status should be 303 Redirect to "/budgets/{budgetId}/transactions"
    And 2 raw transactions should be persisted in "raw_transactions_current"
    And 1 raw transaction should be automatically matched and resolved into "budget_transactions"
    And 1 raw transaction should remain in "raw_transactions_current" with status "PENDING"
