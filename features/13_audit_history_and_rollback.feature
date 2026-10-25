Feature: Entity Version History, Immutable Audit Trail and Temporal Rollback
  As a budget Owner or Admin
  I want to view the chronological version history and field diffs of budget entities in a side drawer
  And restore prior versions of entities with atomic balance projection updates
  So that accidental edits or deletions can be audited and safely rolled back

  Scenario: Viewing entity version history and field diffs in the history drawer
    Given an authenticated user "admin@example.com" with "ADMIN" role on budget "Audit Budget"
    And an account "Main Checking" was created and subsequently updated
    When I request the history drawer for account "Main Checking"
    Then the response status should be 200 OK
    And the history drawer should display version timeline in descending order
    And the history drawer should display the action badges and field diffs
    And the history drawer should provide a restore action for prior versions

  Scenario: Admin successfully restores a prior version of an entity
    Given an authenticated user "admin@example.com" with "ADMIN" role on budget "Audit Budget"
    And a counterparty "Local Bakery" with initial name "Local Bakery" at version 1 and "Super Bakery" at version 2
    When I submit a restore request for party "Local Bakery" to version 1
    Then the response status should be 200 OK
    And a new version 3 with action "RESTORE" should be appended to "parties"
    And "parties_current" should reflect the restored name "Local Bakery" with version 3
    And historical versions 1 and 2 in "parties" should remain immutable

  Scenario: Restoring a budget transaction updates account and bucket balances
    Given an authenticated user "owner@example.com" with "OWNER" role on budget "Audit Budget"
    And a budget transaction of "100.00 PLN" in bucket "Groceries" at version 1
    And the transaction was updated to "300.00 PLN" at version 2
    When I submit a restore request for the budget transaction to version 1
    Then a new version 3 with action "RESTORE" should be appended to "budget_transactions"
    And the transaction amount in "budget_transactions_current" should be "100.00 PLN"
    And the bucket balance for "Groceries" should reflect the restored "100.00 PLN" expense
    And the source account balance should reflect the restored "100.00 PLN" expense

  Scenario: Restoring a soft-deleted entity resurrects it into current projection
    Given an authenticated user "admin@example.com" with "ADMIN" role on budget "Audit Budget"
    And an account "Vacation Fund" was soft-deleted at version 2
    And "Vacation Fund" does not exist in "accounts_current"
    When I submit a restore request for account "Vacation Fund" to version 1
    Then a new version 3 with action "RESTORE" should be appended to "accounts"
    And "Vacation Fund" should be resurrected in "accounts_current" with version 3

  Scenario: Non-admin members cannot restore entity versions
    Given an authenticated user "editor@example.com" with "EDITOR" role on budget "Audit Budget"
    And an account "Family Checking" exists with 2 historical versions
    When "editor@example.com" attempts to restore account "Family Checking" to version 1
    Then the action should be rejected with status 403 Forbidden
    And no new version should be created in "accounts"
