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

  Scenario: Batch deletion of multiple budget transactions from ledger
    Given an active checking account in "Family Home" with starting balance of "500.00 PLN"
    And an active bucket "Office Supplies" with available balance of "500.00 PLN"
    And 3 budget transactions recorded:
      | Note          | Amount     | Bucket          |
      | Desk Lamp     | 100.00 PLN | Office Supplies |
      | Ergonomic Mat | 50.00 PLN  | Office Supplies |
      | Sticky Notes  | 25.00 PLN  | Office Supplies |
    When I batch delete the budget transactions for "Desk Lamp" and "Ergonomic Mat"
    Then the transactions for "Desk Lamp" and "Ergonomic Mat" should no longer appear in the budget transactions ledger
    And the transaction for "Sticky Notes" should still appear in the budget transactions ledger
    And the account balance should be reverted by "150.00 PLN" to "475.00 PLN"
    And the available balance of bucket "Office Supplies" should be restored by "150.00 PLN" to "475.00 PLN"

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

  Scenario: Asynchronous bank sync over SSE notifies user upon completion
    Given an active checking account "mBank Checking" in "Family Home"
    When I trigger an asynchronous bank sync for account "mBank Checking"
    Then the server should return 202 Accepted with a sync in-progress indicator
    And an event stream notification "sync-complete" should be broadcast to the client

  Scenario: Prevent concurrent bank synchronizations on the same budget
    Given an ongoing bank synchronization on budget "Family Home"
    When another synchronization request is submitted for budget "Family Home"
    Then the request should be rejected with 409 Conflict status

  Scenario: Enforce daily bank sync rate limit of 4 imports per day
    Given an account "mBank Checking" that has already performed 4 bank synchronizations today
    When I trigger "Sync with Bank" on budget "Family Home" for account "mBank Checking"
    Then the sync should be rejected with a 429 Rate Limit error explaining the daily quota

  Scenario: Viewing pending raw transactions on the transactions page
    Given an active checking account "mBank Checking" with 6 pending raw transactions
    When I visit the transactions ledger page
    Then I should see a tab or workbench for "Pending Bank Feeds"
    And each raw transaction should display its booking date, raw payee, and amount

  Scenario: Bank synchronization distinguishes booked transactions and pending holds
    Given an active checking account "mBank Checking" linked to a bank feed with booked and pending transactions
    When I trigger "Sync with Bank" on budget "Family Home" for account "mBank Checking"
    Then newly discovered raw transactions should include booked transactions with "booking_status = 'BOOKED'"
    And raw transactions should include pending holds with "booking_status = 'PENDING'"
    And the Reconcile Workbench should display "Pending Hold" badge for pending holds
    And resolving or converting a pending hold transaction into a budget transaction should be blocked

  Scenario: Subsequent bank sync settles pending hold into booked transaction
    Given a pending hold raw transaction in account "mBank Checking"
    When the bank subsequently reports the transaction as cleared and booked
    And I trigger "Sync with Bank" on budget "Family Home" for account "mBank Checking"
    Then the pending raw transaction should transition to "booking_status = 'BOOKED'"
    And it should now be resolvable in the Reconcile Workbench

  Scenario: Direct Move resolution of a single raw transaction into budget transaction
    Given a pending raw transaction of "-50.00 PLN" from "Żabka" in account "mBank Checking"
    When I resolve the raw transaction into bucket "Groceries"
    Then the raw transaction status should change to "PROCESSED"
    And a budget transaction of "50.00 PLN" should appear in the budget transactions ledger

  Scenario: Reconcile Workbench allows batch Auto-Match with matching rules
    Given a matching rule mapping "Żabka" to bucket "Groceries"
    And a pending raw transaction of "-50.00 PLN" from "Żabka" in account "mBank Checking"
    When I trigger "Auto-Match" in the workbench
    Then the raw transaction status should change to "PROCESSED"
    And a budget transaction of "50.00 PLN" should appear in the budget transactions ledger

  Scenario: Permanently delete a pending raw transaction feed
    Given an active checking account in "Family Home"
    And a pending raw transaction of "-42.00 PLN" from "Bookstore" in account "Family Checking"
    When I delete the pending raw transaction
    Then the raw transaction should be permanently removed from raw transactions

  Scenario: Batch delete multiple pending raw transaction feeds
    Given an active checking account in "Family Home"
    And 2 pending raw transactions recorded:
      | Payee      | Amount     | Account         |
      | Coffee Shop| -15.00 PLN | Family Checking |
      | Bakery     | -25.00 PLN | Family Checking |
    When I batch delete the pending raw transactions
    Then the raw transactions should be permanently removed from raw transactions

  Scenario: Attempting to sync an unlinked cash account rejects and creates no mock transactions
    Given an active cash account "Wallet Cash" with no bank integration
    When I trigger "Sync with Bank" on budget "Family Home" for account "Wallet Cash"
    Then the bank sync should fail with an unlinked account error
    And no raw transactions should be generated for "Wallet Cash"

  Scenario: Bank sync modal lists only accounts linked to a bank feed
    Given an active checking account "mBank Checking" with 0.00 PLN initial balance
    And an active cash account "Wallet Cash" with no bank integration
    When I visit the transactions ledger page
    Then the bank sync modal should list "mBank Checking"
    And the bank sync modal should not list "Wallet Cash"

  Scenario: Sync with Bank button is disabled when no bank accounts are linked
    Given a budget "Empty Budget" with only manual cash accounts
    When I visit the transactions ledger page on budget "Empty Budget"
    Then the Sync with Bank button should be disabled with empty state notice

  Scenario: Mock bank synchronization is blocked in production mode
    Given an active checking account "mBank Checking" with 0.00 PLN initial balance
    And the application is running in production mode
    When I trigger "Sync with Bank" on budget "Family Home" for account "mBank Checking"
    Then the bank sync should fail with a mock disabled in production error




