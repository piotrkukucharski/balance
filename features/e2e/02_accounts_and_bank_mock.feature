@e2e
Feature: Browser E2E Accounts Management and Mock Bank Integration Wizard
  As a budget owner in a web browser
  I want to create manual accounts with custom icons, connect bank accounts via Mock Sandbox, and reconnect expired integrations
  So that all my accounts and balances are accurately tracked in the UI

  Scenario: Creating a manual checking account with emoji favicon and toggling close status
    Given an authenticated browser session for "jan.e2e@example.com" on budget "Family Home"
    When I navigate to the "Accounts" view in the browser
    And I open the "New Account" modal and create a "CHECKING" account named "Cash Wallet" with currency "PLN", balance "1500.00" and emoji icon "💵"
    Then the accounts table should display "Cash Wallet" with icon "💵" and balance "1,500.00 PLN"
    When I open the edit modal for account "Cash Wallet" and change its emoji icon to "🏦"
    Then the accounts table should display "Cash Wallet" with icon "🏦"
    When I trigger the close toggle action for account "Cash Wallet"
    Then the account "Cash Wallet" should display status badge "Closed"

  Scenario: Connecting multiple bank institutions sequentially via the Mock Sandbox wizard
    Given an authenticated browser session for "jan.e2e@example.com" on budget "Family Home"
    When I navigate to the "Accounts" view in the browser
    And I open the "Connect Bank Accounts" modal
    And I select the "Mock Sandbox" provider in step 1
    And I select country "Poland" in step 2
    And I select institutions "Sandbox Finance" and "mBank Mock Retail" in step 3 and click connect
    Then the sequential connection queue in step 4 should complete for both institutions
    When I click "Done & View Accounts"
    Then the accounts table should display the imported mock checking and savings accounts with their balances

  Scenario: Reconnecting a bank account with expired consent from the Accounts UI
    Given an authenticated browser session for "jan.e2e@example.com" on budget "Family Home"
    And a linked mock bank account "mBank Checking" with expired authorization consent
    When I navigate to the "Accounts" view in the browser
    Then the account "mBank Checking" should display the "Consent Expired" warning badge
    When I click the "Reconnect" button for "mBank Checking"
    Then the account "mBank Checking" integration status should be restored to active without "Consent Expired"
