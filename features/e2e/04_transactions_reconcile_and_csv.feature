@e2e
Feature: Browser E2E Transactions CRUD, Bank Sync, Reconcile Workbench and CSV Import
  As a budget user in a web browser
  I want to record manual transactions, sync with the bank, resolve raw transactions via Move/Merge/Split/Auto-Match, and import CSV statements
  So that all bank activity is reconciled with my budget envelopes

  Scenario: Recording, editing and deleting a manual transaction in the browser
    Given an authenticated browser session for "jan.e2e@example.com" on budget "Family Home" with account "Main Checking" and bucket "Groceries"
    When I navigate to the "Transactions" view in the browser
    And I submit the manual transaction form with account "Main Checking", bucket "Groceries", amount "-150.00", date "2026-10-16" and note "Weekly groceries"
    Then the transactions ledger table should display "Weekly groceries" with amount "-150.00 PLN"
    When I open the edit modal for transaction "Weekly groceries" and update the note to "Updated groceries" and amount to "-120.00"
    Then the transactions ledger table should display "Updated groceries" with amount "-120.00 PLN"
    When I delete the transaction "Updated groceries" from the actions menu
    Then "Updated groceries" should no longer appear in the transactions ledger table

  Scenario: Synchronizing with Mock Bank and resolving pending raw transactions via Move, Merge, Split and Auto-Match
    Given an authenticated browser session for "jan.e2e@example.com" on budget "Family Home"
    And a linked mock bank account "mBank Checking" and buckets "Groceries", "Dining Out" and "Household"
    And an active matching rule mapping "ZABKA" to bucket "Groceries"
    When I navigate to the "Transactions" view in the browser
    And I open the "Sync with Bank" modal and trigger bank synchronization for "mBank Checking"
    Then pending raw bank transactions should appear in the Reconcile Workbench
    When I click "Auto-Match" in the Reconcile Workbench
    Then the "ZABKA" raw transactions should be automatically resolved into bucket "Groceries"
    When I select the 3 dining bill and friend repayment raw transactions and execute a "Merge" resolution into bucket "Dining Out"
    Then a single net budget transaction of "-100.00 PLN" should appear in the ledger for bucket "Dining Out"
    When I open the "Split" modal for a pending raw transaction and split it across "Groceries" and "Household"
    Then the split budget transactions should appear in the ledger

  Scenario: Previewing and importing a bank statement CSV file in the browser
    Given an authenticated browser session for "jan.e2e@example.com" on budget "Family Home" with account "Main Checking" and bucket "Groceries"
    And an active matching rule mapping "Biedronka" to bucket "Groceries"
    When I navigate to "/budgets/:budgetId/transactions/import-csv" in the browser
    And I select account "Main Checking", upload a CSV statement with "Biedronka" and "Employer Sp. z o.o." rows, and click preview
    Then the CSV preview table should display 2 parsed rows in the browser
    When I confirm and submit the CSV import with auto-match enabled
    Then the browser should redirect to the transactions view
    And the "Biedronka" transaction should appear in the resolved budget transactions ledger
    And the "Employer Sp. z o.o." transaction should appear in the Pending Bank Feeds workbench
