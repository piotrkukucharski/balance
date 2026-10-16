Feature: Two-Tier Transaction Resolution (Move, Merge, Split) and Deduplication
  As a budget user
  I want to import bank transactions and reconcile them into budget transactions
  So that ledger truth matches budgetary truth without double counting

  Scenario: Synchronizing transactions from bank feed with deduplication
    Given an active checking account linked to the Mock bank provider
    When I trigger "Sync with Bank" on budget "Family Home"
    Then newly discovered raw transactions should be imported into "raw_transactions_current" with status "PENDING"
    And each raw transaction should have a length-prefixed canonical SHA-256 dedup hash
    And re-running the sync should safely skip existing transactions without duplicates

  Scenario: N-to-1 Merge resolution for split dining bill
    Given 3 pending raw transactions in account "User Checking":
      | RawID | Amount     | Payee                    |
      | raw_1 | -300.00 PLN| RESTAURACJA POD BARANEM  |
      | raw_2 | +100.00 PLN| MAREK KOWALSKI           |
      | raw_3 | +100.00 PLN| ANNA NOWAK               |
    When I execute a "MERGE" resolution into a single budget transaction of "100.00 PLN" assigned to bucket "Dining Out"
    Then the 3 raw transactions should transition to status "PROCESSED"
    And 1 new budget transaction of "100.00 PLN" should be inserted into "budget_transactions_current"
    And 3 audit records should be created in "transaction_links" with a shared "operation_id"
    And the balance of "User Checking" should reflect a net outflow of 100.00 PLN
    And the available balance of bucket "Dining Out" should be reduced by 100.00 PLN

  Scenario: 1-to-N Split resolution for supermarket receipt
    Given a pending raw transaction of "-400.00 PLN" from "Supermarket"
    When I execute a "SPLIT" resolution into two budget transactions:
      | Bucket     | Amount     |
      | Groceries  | 300.00 PLN |
      | Household  | 100.00 PLN |
    Then the raw transaction should transition to status "PROCESSED"
    And two distinct budget transactions should be created and linked under the operation

  Scenario: Manual creation, updating, and deletion (CRUD) of budget transactions
    Given an active checking account in "Family Home"
    When I create a manual transaction with amount "150.00 PLN", date "2026-10-16", and note "Office supplies"
    Then the transaction should appear in the budget transactions ledger with amount "150.00 PLN"
    When I edit the transaction notes to "Updated office supplies" and amount to "120.00 PLN"
    Then the transaction ledger should reflect note "Updated office supplies" and amount "120.00 PLN"
    When I delete the transaction
    Then the transaction should no longer appear in the budget transactions ledger

  Scenario: First bank sync calculates Set Balance offset based on imported window
    Given an active checking account "mBank Checking" with 0.00 PLN initial balance
    And the external bank account currently has a balance of "5,420.50 PLN"
    When I trigger "Sync with Bank" on budget "Family Home" for account "mBank Checking" with a 30-day window
    Then newly discovered raw transactions should be imported into "raw_transactions_current" with status "PENDING"
    And a budget transaction for "Set Balance Offset" should be recorded with the date of the oldest transaction
    And the amount of "Set Balance Offset" should equal the bank balance minus the net sum of imported raw transactions
    And the balance of "mBank Checking" should equal the starting balance ready to assign

  Scenario: Subsequent bank sync only fetches transactions newer than the latest imported transaction
    Given an active checking account "mBank Checking" with previously imported transactions
    When I trigger "Sync with Bank" on budget "Family Home" for account "mBank Checking"
    Then the bank sync should query transactions starting strictly after the latest imported booking date
    And duplicate transactions should not be inserted


