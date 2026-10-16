Feature: Accounts Management, Multi-Currency Balances and Bank Integration Links
  As a budget Editor or Owner
  I want to create manual accounts, view multi-currency balances, and link bank feeds
  So that all user-owned liquidity is accurately represented

  Scenario: Creating a manual checking account with initial balance
    Given an authenticated user "jan@example.com" on budget "Family Home"
    When I create an account named "mBank Main Checking" of type "CHECKING" with currency "PLN" and initial balance "1,500.00 PLN"
    Then the account should be stored in "accounts" and projected in "accounts_current"
    And the owning party should be marked with "is_user_owned = 1"
    And the balance in "account_currency_balances" for currency "PLN" should be 150000 minor units

  Scenario: Linking a bank account integration
    Given an authenticated user "jan@example.com" on budget "Family Home"
    And an available discovered account "mock_acc_checking_pln" from the bank provider
    When I link "mock_acc_checking_pln" with label "Linked Checking Account"
    Then an entry should be added to "account_integration_links"
    And the account initial balance should be updated from the provider statement

  Scenario: Toggling account active and closed status
    Given an active account "Old Savings" in budget "Family Home"
    When I toggle the close status on "Old Savings"
    Then the "is_closed" flag in "accounts_current" should be updated to 1
    And the account should appear as closed in the accounts view

  Scenario: Starting integrated bank account connection from accounts view
    Given an authenticated user "jan@example.com" on budget "Family Home"
    When I open the link bank flow modal
    Then I should see available bank providers "Mock Sandbox" and "GoCardless"

  Scenario: Selecting bank provider and country in link bank flow
    Given an authenticated user "jan@example.com" on budget "Family Home"
    When I select provider "MOCK" and country "PL"
    Then the system should list available institutions for "PL"

  Scenario: Selecting multiple bank institutions executes sequential connection queue
    Given an authenticated user "jan@example.com" on budget "Family Home"
    When I select multiple institutions "SANDBOXFINANCE_SFIN0000" and "MOCK_BANK_PL01" to connect
    Then the connection queue should initialize with 2 pending institutions
    And connecting the first institution should link its discovered accounts to the budget
    And the flow should automatically advance to the next institution in the queue

  Scenario: Displaying consent expired warning on accounts view
    Given an authenticated user "jan@example.com" on budget "Family Home"
    And a linked bank account "mBank Checking" with expired bank authorization
    When I request the accounts view "/budgets/:budgetId/accounts"
    Then the response status should be 200 OK
    And the account status badge should indicate "Consent Expired"
    And a "Reconnect" action button should be rendered for the account

  Scenario: Reconnecting an expired bank account integration
    Given an authenticated user "jan@example.com" on budget "Family Home"
    And a linked bank account "mBank Checking" with expired bank authorization
    When I trigger reconnect for the expired account "mBank Checking"
    Then a new authorization session should be created preserving existing account transactions
    And the integration status should transition to "ACTIVE" or "awaiting_authorization"

  Scenario: Successfully processing GoCardless OAuth callback and importing accounts
    Given an authenticated user "jan@example.com" on budget "Family Home"
    And a pending GoCardless requisition for institution "MBANK_PL"
    When the GoCardless OAuth callback is received with valid requisition reference
    Then the bank accounts from the requisition should be imported into the budget
    And the integration status should be marked as "ACTIVE"
    And the browser should be redirected to the budget accounts view

