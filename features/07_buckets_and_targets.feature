Feature: Buckets, Targets, Health Statuses and Waterfall Auto-Assign
  As a budget user
  I want to create envelopes, assign priority targets, and view health status indicators
  So that I know at a glance if my budget is funded and healthy

  Scenario: Creating a bucket with multiple targets and priorities
    Given an open budget "Family Home"
    When I create a bucket "Living Expenses" in group "Needs"
    And I add a mandatory target "Rent" with priority 3, cadence "MONTHLY", amount "2,000.00 PLN"
    And I add a recommended target "Electricity" with priority 2, cadence "MONTHLY", amount "300.00 PLN"
    Then the bucket and targets should be recorded in event streams and projections

  Scenario: Creating a target with custom days recurrence interval
    Given an open budget "Family Home"
    When I create a bucket "Subscriptions" in group "Needs"
    And I add a target "Quarterly Pass" with priority 2, cadence "CUSTOM_DAYS", recurrence days 90, amount "125.00 PLN"
    Then target "Quarterly Pass" in projections should have recurrence days 90

  Scenario: Creating a target with custom months and custom years cadence
    Given an open budget "Family Home"
    When I create a bucket "Insurance" in group "Needs"
    And I add a target "Quarterly Health Insurance" with priority 2, cadence "CUSTOM_MONTHS", recurrence interval 3, due in 90 days, amount "450.00 PLN"
    And I add a target "Annual Car Insurance" with priority 3, cadence "CUSTOM_YEARS", recurrence interval 1, due in 180 days, amount "1,200.00 PLN"
    Then target "Quarterly Health Insurance" in projections should have cadence "CUSTOM_MONTHS" and recurrence interval 3
    And target "Annual Car Insurance" in projections should have cadence "CUSTOM_YEARS" and recurrence interval 1

  Scenario: Creating periodic targets with optional anchor due date
    Given an open budget "Family Home"
    When I create a bucket "Recurring Bills" in group "Needs"
    And I add a target "Weekly Cleaners" with priority 2, cadence "WEEKLY", recurrence interval 0, due in 3 days, amount "150.00 PLN"
    And I add a target "Monthly Internet" with priority 3, cadence "MONTHLY", recurrence interval 0, due in 15 days, amount "80.00 PLN"
    And I add a target "Annual Domain" with priority 1, cadence "ANNUAL", recurrence interval 0, due in 120 days, amount "100.00 PLN"
    Then target "Weekly Cleaners" in projections should have cadence "WEEKLY" and non-zero due date
    And target "Monthly Internet" in projections should have cadence "MONTHLY" and non-zero due date
    And target "Annual Domain" in projections should have cadence "ANNUAL" and non-zero due date

  Scenario: Updating targets and validating deletion constraints on buckets
    Given an open budget "Family Home"
    And an existing bucket "Subscriptions" in group "Needs" with funded balance "100.00 PLN"
    And an existing target "Streaming" in bucket "Subscriptions" with required amount "50.00 PLN"
    When I update target "Streaming" amount to "60.00 PLN" and priority to 1
    Then the target requirement in projections should be "60.00 PLN"
    When I attempt to archive bucket "Subscriptions"
    Then the bucket archiving should be rejected because it has active balance or targets
    When I delete target "Streaming"
    And I move "100.00 PLN" from bucket "Subscriptions" to ready to assign
    And I archive bucket "Subscriptions"
    Then the bucket should be marked archived in projections

  Scenario Outline: Visualizing YNAB-style Bucket Health Status
    Given a bucket "<Bucket>" with funded balance "<Funded>" and required target amount "<Required>"
    When the budget dashboard evaluates the bucket health
    Then the health status badge should display "<Badge>"

    Examples:
      | Bucket     | Funded       | Required     | Badge               |
      | Rent       | 2,000.00 PLN | 2,000.00 PLN | Funded / On Track   |
      | Holidays   | 400.00 PLN   | 1,000.00 PLN | Underfunded         |
      | Dining Out | -50.00 PLN   | 0.00 PLN     | Overspent           |

  Scenario: Dynamic Ready to Assign calculation and manual assignment
    Given an open budget "Family Home" with "5,000.00 PLN" in checking account
    And an existing bucket "Groceries" with funded balance "1,200.00 PLN"
    When I request the budget view "/budgets/:budgetId/budget"
    Then the Ready to Assign banner should display "3,800.00 PLN"
    When I assign "800.00 PLN" from ready to assign to bucket "Groceries"
    Then the bucket "Groceries" balance should be "2,000.00 PLN"
    And the Ready to Assign banner should display "3,000.00 PLN"

  Scenario: Priority Waterfall Auto-Assigning available pool
    Given an open budget "Family Home" with "2,200.00 PLN" in checking account
    And a bucket "Fixed Expenses" with the following targets in the active 30-day window:
      | Target      | Priority | Needed       |
      | Rent        | 3        | 2,000.00 PLN |
      | Utilities   | 3        | 300.00 PLN   |
      | Dining Out  | 2        | 400.00 PLN   |
      | Gadgets     | 1        | 100.00 PLN   |
    When I trigger "Auto-Assign by Priority"
    Then target "Rent" should receive "2,000.00 PLN"
    And target "Utilities" should receive "200.00 PLN"
    And target "Dining Out" should receive "0.00 PLN"
    And target "Gadgets" should receive "0.00 PLN"
    And the unallocated pool should be reduced to "0.00 PLN"

  Scenario: Reverse-waterfall unassigning funds from bucket reduces target funded amount
    Given an open budget "Family Home" with "5,000.00 PLN" in checking account
    When I create a bucket "Home Insurance" in group "Needs"
    And I add a mandatory target "Home Coverage" with priority 3, cadence "ANNUAL", amount "600.00 PLN"
    And I assign "600.00 PLN" from ready to assign to bucket "Home Insurance"
    Then the bucket "Home Insurance" balance should be "600.00 PLN"
    And target "Home Coverage" in bucket "Home Insurance" should have funded amount "600.00 PLN"
    When I move "600.00 PLN" from bucket "Home Insurance" to ready to assign
    Then the bucket "Home Insurance" balance should be "0.00 PLN"
    And target "Home Coverage" in bucket "Home Insurance" should have funded amount "0.00 PLN"

  Scenario: Reverse-waterfall unassigning funds defunds lower priority targets first
    Given an open budget "Family Home" with "5,000.00 PLN" in checking account
    When I create a bucket "Leisure" in group "Wants"
    And I add a mandatory target "Gym" with priority 3, cadence "MONTHLY", amount "400.00 PLN"
    And I add an optional target "Cinema" with priority 1, cadence "MONTHLY", amount "200.00 PLN"
    And I assign "600.00 PLN" from ready to assign to bucket "Leisure"
    Then target "Gym" in bucket "Leisure" should have funded amount "400.00 PLN"
    And target "Cinema" in bucket "Leisure" should have funded amount "200.00 PLN"
    When I move "200.00 PLN" from bucket "Leisure" to ready to assign
    Then the bucket "Leisure" balance should be "400.00 PLN"
    And target "Cinema" in bucket "Leisure" should have funded amount "0.00 PLN"
    And target "Gym" in bucket "Leisure" should have funded amount "400.00 PLN"

  Scenario: Moving funds between buckets updates target funding
    Given an open budget "Family Home" with "5,000.00 PLN" in checking account
    When I create a bucket "Source Envelope" in group "Needs"
    And I add a mandatory target "Source Target" with priority 3, cadence "MONTHLY", amount "500.00 PLN"
    And I create a bucket "Dest Envelope" in group "Needs"
    And I add a mandatory target "Dest Target" with priority 3, cadence "MONTHLY", amount "500.00 PLN"
    And I assign "500.00 PLN" from ready to assign to bucket "Source Envelope"
    Then target "Source Target" in bucket "Source Envelope" should have funded amount "500.00 PLN"
    When I move "500.00 PLN" from bucket "Source Envelope" to bucket "Dest Envelope"
    Then the bucket "Source Envelope" balance should be "0.00 PLN"
    And target "Source Target" in bucket "Source Envelope" should have funded amount "0.00 PLN"
    And the bucket "Dest Envelope" balance should be "500.00 PLN"
    And target "Dest Target" in bucket "Dest Envelope" should have funded amount "500.00 PLN"

  Scenario: Evaluating bucket unassigned buffer and health status with over-allocation
    Given an open budget "Family Home" with "5,000.00 PLN" in checking account
    When I create a bucket "Car Maintenance" in group "Needs"
    And I add a mandatory target "Oil Change" with priority 3, cadence "MONTHLY", amount "300.00 PLN"
    And I assign "400.00 PLN" from ready to assign to bucket "Car Maintenance"
    Then the bucket "Car Maintenance" should have unassigned buffer "100.00 PLN"
    And the bucket "Car Maintenance" should have health status "Funded / On Track"

  Scenario: Evaluating bucket health status with optional target remaining
    Given an open budget "Family Home" with "5,000.00 PLN" in checking account
    When I create a bucket "Hobbies" in group "Wants"
    And I add an optional target "Books" with priority 1, cadence "MONTHLY", amount "100.00 PLN"
    Then the bucket "Hobbies" should have health status "Optional remaining"

  Scenario: Evaluating bucket health status with over-allocated buffer
    Given an open budget "Family Home" with "5,000.00 PLN" in checking account
    When I create a bucket "Pet Care" in group "Needs"
    And I add a mandatory target "Vet" with priority 3, cadence "MONTHLY", amount "300.00 PLN"
    And target "Vet" in bucket "Pet Care" has funded amount "300.00 PLN" with bucket balance "200.00 PLN"
    Then the bucket "Pet Care" should have unassigned buffer "-100.00 PLN"
    And the bucket "Pet Care" should have health status "Over-allocated"
    And the bucket "Pet Care" should be marked with P3 at risk

  Scenario: Creating a target in an available non-default currency and enforcing currency immutability
    Given an open budget "Family Home" with "5,000.00 PLN" in checking account
    And an account "EUR Savings" with currency "EUR" and balance "1,000.00 EUR"
    When I create a bucket "Euro Travel" in group "Wants"
    And I add a target "Hotel Reservation" with priority 2, cadence "MONTHLY", amount "250.00 EUR" in currency "EUR"
    Then target "Hotel Reservation" in projections should have currency "EUR" and amount "250.00 EUR"
    When I attempt to update target "Hotel Reservation" with currency "USD"
    Then target "Hotel Reservation" in projections should still have currency "EUR"

  Scenario: Rejecting target creation with currency not available in budget
    Given an open budget "Family Home" with "5,000.00 PLN" in checking account
    When I create a bucket "US Shopping" in group "Wants"
    And I attempt to add a target "Amazon US" in currency "USD"
    Then the target creation should be rejected due to unsupported currency

  Scenario: Continuous pro-rated target calculation for recurring cycles across horizon windows
    Given an open budget "Family Home" with "5,000.00 PLN" in checking account
    When I create a bucket "Subscriptions" in group "Needs"
    And I add a recurring target "Streaming" with amount "100.00 PLN" monthly and due in 12 days
    Then the target requirement in a 30-day window should be "160.00 PLN"

  Scenario: Actions dropdown menu in buckets and accounts list supports upwards opening and anchor positioning
    Given an open budget "Family Home"
    When I create a bucket "Living Expenses" in group "Needs"
    And I request the budget view "/budgets/:budgetId/budget"
    Then the response status should be 200 OK
    And the action menu popover should support upward positioning and anchor try fallbacks

  Scenario: Creating and displaying a target with optional name and showing only priority
    Given an open budget "Family Home"
    When I create a bucket "Emergency Fund" in group "Savings"
    And I add a target with priority 3, cadence "MONTHLY", amount "500.00 PLN" without a name
    Then the target in bucket "Emergency Fund" with priority 3 should have an empty or null name
    And the budget page for "Family Home" should display priority badge "Mandatory" without target name

  Scenario: Renaming category groups and changing their order
    Given an open budget "Family Home"
    When I create a bucket "Rent" in group "Fixed Costs"
    And I create a bucket "Groceries" in group "Living"
    And I request to move category group "Living" up
    Then category group "Living" should precede "Fixed Costs" in budget view
    When I request to rename category group "Living" to "Daily Life"
    Then category group "Daily Life" should precede "Fixed Costs" in budget view
    And bucket "Groceries" in projections should have group "Daily Life"

  Scenario: Datalist of existing category groups for new and edited buckets
    Given an open budget "Family Home"
    When I create a bucket "Rent" in group "Housing"
    And I create a bucket "Groceries" in group "Daily Expenses"
    And I create a bucket "Savings" in group "Future"
    And I request the budget view "/budgets/:budgetId/budget"
    Then the response status should be 200 OK
    And the new bucket modal should contain a datalist with existing category groups "Housing", "Daily Expenses", "Future"
    And the new bucket group input should have an empty value and placeholder containing the first categories
    And the edit bucket group input should be bound to the existing category groups datalist

  Scenario: Moving bucket to another category group via drag and drop endpoint
    Given an open budget "Family Home"
    When I create a bucket "Groceries" in group "Living"
    And I request the budget view "/budgets/:budgetId/budget"
    Then the response status should be 200 OK
    And bucket "Groceries" in projections should have group "Living"
    When I move bucket "Groceries" to category group "Food & Dining"
    Then the response status should be 200 OK
    And bucket "Groceries" in projections should have group "Food & Dining"
    And I request the budget view "/budgets/:budgetId/budget"
    Then the response status should be 200 OK
    And the desktop bucket row "Groceries" should have a drag handle
    And category group "Food & Dining" should be a drop target for buckets







