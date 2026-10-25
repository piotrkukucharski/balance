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
