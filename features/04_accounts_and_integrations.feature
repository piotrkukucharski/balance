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
