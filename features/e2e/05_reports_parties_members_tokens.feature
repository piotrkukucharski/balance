@e2e
Feature: Browser E2E Interactive Reports, External Parties, Members and Personal Access Tokens
  As a budget user in a web browser
  I want to explore the 10 ECharts analytics reports, save custom report views, add counterparties, invite members, and manage PAT tokens
  So that all analytics, governance, and API access features work seamlessly end-to-end

  Scenario: Viewing the 10 ECharts report cards, opening a detailed report, and saving and pinning a custom configuration
    Given an authenticated browser session for "jan.e2e@example.com" on budget "Family Home" with rich sample transactions
    When I navigate to the "Reports" view in the browser
    Then the dashboard should render all 10 report cards including "Age of Money", "Income v. Expense", "Spending Breakdown" and "Monte Carlo Simulator"
    When I click on the "Spending Breakdown" report card to open the detailed report view
    Then the detailed report page should render filter controls and initialize the ECharts chart canvas
    When I open the "Save Report Configuration" modal, save a report named "My Q4 Breakdown" as pinned, and return to "/budgets/:budgetId/reports"
    Then the "Saved Report Configurations" section should display "My Q4 Breakdown" with a pinned badge
    When I unpin and delete the saved report "My Q4 Breakdown"
    Then "My Q4 Breakdown" should be removed from the saved reports section

  Scenario: Adding an external counterparty, inviting a budget collaborator, and generating and revoking a Personal Access Token
    Given an authenticated browser session for "jan.e2e@example.com" on budget "Family Home"
    When I navigate to the "External Parties" view in the browser
    And I open the "New Counterparty" modal and create an "ORGANIZATION" party named "Żabka Polska"
    Then the parties table should display "Żabka Polska" with badge "External Counterparty"
    When I navigate to the "Members" settings view in the browser
    And I open the "Invite Member" modal and invite "marek.collab@example.com" with role "EDITOR"
    Then the browser should complete the invitation submission without errors and render the members table
    When I navigate to the "API Tokens" settings view in the browser
    And I generate a new Personal Access Token named "E2E CLI Token" with "30 days" expiration
    Then the token created modal should open displaying the generated "bal_pat_" token value
    When I close the token modal and click "Revoke" on "E2E CLI Token"
    Then the token "E2E CLI Token" row should display status badge "Revoked"
