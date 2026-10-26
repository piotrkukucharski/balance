@e2e
Feature: Browser E2E Envelope Buckets, Targets, Fund Assignments and Priority Auto-Assign
  As a budget user in a web browser
  I want to create buckets, configure priority targets in the drawer, assign or move funds, and run priority auto-assign
  So that my Ready to Assign pool is allocated across envelopes according to YNAB zero-sum rules

  Scenario: Creating buckets, adding targets in the Bucket Drawer, and assigning and moving funds
    Given an authenticated browser session for "jan.e2e@example.com" on budget "Family Home" with "5,000.00 PLN" in "Main Checking"
    When I navigate to the "Budget" workspace view in the browser
    Then the Ready to Assign banner should display "5,000.00 PLN"
    When I open the "New Bucket" modal and create bucket "Living Expenses" in group "Needs"
    And I open the "New Bucket" modal and create bucket "Leisure" in group "Wants"
    Then the budget table should list buckets "Living Expenses" and "Leisure"
    When I open the Bucket Inspector drawer for "Living Expenses"
    And I click "+ Add Target" and create a "MONTHLY" target named "Apartment Rent" with priority "Mandatory (3)" and amount "2000.00"
    Then the bucket "Living Expenses" should display required target "2,000.00 PLN" and health badge "Underfunded"
    When I open the "Assign" modal for "Living Expenses" and assign "2500.00" PLN
    Then the bucket "Living Expenses" available balance should be "2,500.00 PLN" and health badge should be "Funded / On Track"
    And the Ready to Assign banner should display "2,500.00 PLN"
    When I open the "Move" modal from "Living Expenses" and move "500.00" PLN to bucket "Leisure"
    Then the bucket "Living Expenses" available balance should be "2,000.00 PLN"
    And the bucket "Leisure" available balance should be "500.00 PLN"

  Scenario: Running Priority Waterfall Auto-Assign from the Budget workspace
    Given an authenticated browser session for "jan.e2e@example.com" on budget "Family Home" with "2,200.00 PLN" in "Main Checking"
    And bucket "Fixed Costs" has mandatory target "Rent" of "2,000.00 PLN" and recommended target "Internet" of "400.00 PLN"
    When I navigate to the "Budget" workspace view in the browser
    And I click the "Auto-Assign by Priority" button
    Then the Ready to Assign banner should display "0.00 PLN"
    And the bucket "Fixed Costs" available balance should be "2,200.00 PLN"

  Scenario: Mobile viewport ergonomics for horizon select, bucket cards, and bottom-sheet assign modal
    Given an authenticated browser session for "jan.e2e@example.com" on budget "Family Home" with "1,500.00 PLN" in "Main Checking"
    And bucket "Fixed Costs" has mandatory target "Rent" of "1,000.00 PLN" and recommended target "Internet" of "200.00 PLN"
    When I resize the browser viewport to mobile 375x812 and navigate to the "Budget" workspace view
    Then the mobile horizon selector and bottom dock should be visible in the mobile viewport
    When I click the quick "Assign" button on the mobile bucket card for "Fixed Costs" and assign "600.00" PLN
    Then the bucket "Fixed Costs" available balance should be "600.00 PLN"

