@e2e
Feature: Browser E2E Authentication, Budget Provisioning and Workspace Navigation
  As a user interacting with Balance through a real web browser
  I want to register, sign in, create and manage budgets, and navigate workspace views
  So that my personal finance workspace is accessible and responsive without console or HTTP errors

  Scenario: Full user onboarding from registration to creating and managing a budget
    Given a clean browser session
    When I navigate to "/register" in the browser
    And I fill in the registration form with display name "Jan Kowalski", email "jan.e2e@example.com" and password "SuperSecret123!"
    And I click the "Get Started Free" submit button
    Then the browser URL should be "/budgets"
    And the page should display "Your Budgets"
    When I open the new budget modal and create a budget named "Family Home" with currency "PLN" and timezone "Europe/Warsaw"
    Then the browser should navigate to the budget workspace view
    And the page should display budget title "Family Home"
    And the Ready to Assign banner should display "0.00 PLN"
    And the sidebar navigation should display links for "Budget", "Accounts", "Transactions", "Reports", "External Parties", and "Settings"

  Scenario: Updating budget settings, renaming budget from list, and logging out and back in
    Given an authenticated browser session for "jan.e2e@example.com" with password "SuperSecret123!" owning budget "Family Home"
    When I navigate to the budget settings page via the sidebar
    And I update the budget name to "Family Home 2026" and save settings
    Then the page should reflect the updated budget name "Family Home 2026"
    When I navigate to "/budgets" in the browser
    Then the budgets list should display "Family Home 2026"
    When I click the "Log Out" button in the top navigation bar
    Then the browser URL should be "/login"
    When I fill in the login form with email "jan.e2e@example.com" and password "SuperSecret123!" and click "Sign In"
    Then the browser URL should be "/budgets"
    And the budgets list should display "Family Home 2026"

  Scenario: Cloning a budget from budgets list with selective resources in browser
    Given an authenticated browser session for "jan.e2e@example.com" with password "SuperSecret123!" owning budget "Family Home 2026"
    When I navigate to "/budgets" in the browser
    And I clone the budget "Family Home 2026" into "Family Home Cloned" via the clone modal
    Then the browser should navigate to the budget workspace view
    And the page should display budget title "Family Home Cloned"
    When I navigate to "/budgets" in the browser
    Then the budgets list should display "Family Home Cloned"
