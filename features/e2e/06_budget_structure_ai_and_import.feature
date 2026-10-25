@e2e
Feature: Browser E2E Envelope Structure Generation with AI and Budget Import
  As a budget user in a web browser
  I want to generate envelopes and targets from natural language descriptions or import them from another budget
  So that I can quickly set up my budget structure without manual configuration

  Scenario: Generating envelopes and targets from natural language description with AI
    Given an authenticated browser session for "jan.e2e@example.com" on budget "Startup Budget"
    When I navigate to the "Budget" workspace view in the browser
    Then the budget table should display the empty structure state card
    When I open the AI Structure generator modal
    And I enter budget prompt "Czynsz 2500 PLN, jedzenie 1200 PLN, oszczędności 500 PLN" and click "Generate Preview"
    Then the structure preview should list proposed envelopes "Czynsz", "Jedzenie", and "Oszczędności"
    When I click "Apply Selected Structure"
    Then the budget table should list buckets "Czynsz", "Jedzenie", and "Oszczędności"

  Scenario: Importing budget structure from another budget with seamless bucket reuse
    Given an authenticated browser session for "jan.e2e@example.com" on budget "Source Household"
    And bucket "Utilities" has target "Electricity" of "200.00 PLN"
    And bucket "Groceries" has target "Food" of "1500.00 PLN"
    When I navigate to "/budgets" in the browser
    And I open the new budget modal and create a budget named "Second Apartment" with currency "PLN" and timezone "Europe/Warsaw"
    And I navigate to the "Budget" workspace view in the browser
    Then the budget table should display the empty structure state card
    When I open the Structure Import modal
    And I select source budget "Source Household" and click "Load Structure"
    Then the structure preview should list proposed envelopes "Utilities" and "Groceries"
    When I click "Apply Selected Structure"
    Then the budget table should list buckets "Utilities" and "Groceries"
