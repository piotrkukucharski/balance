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

  Scenario Outline: Visualizing YNAB-style Bucket Health Status
    Given a bucket "<Bucket>" with funded balance "<Funded>" and required target amount "<Required>"
    When the budget dashboard evaluates the bucket health
    Then the health status badge should display "<Badge>"

    Examples:
      | Bucket     | Funded       | Required     | Badge               |
      | Rent       | 2,000.00 PLN | 2,000.00 PLN | Funded / On Track   |
      | Holidays   | 400.00 PLN   | 1,000.00 PLN | Underfunded         |
      | Dining Out | -50.00 PLN   | 0.00 PLN     | Overspent           |

  Scenario: Priority Waterfall Auto-Assigning available pool
    Given an unallocated pool of "2,200.00 PLN"
    And the following targets in the active 30-day window:
      | Target      | Priority | Needed       |
      | Rent        | 3        | 2,000.00 PLN |
      | Utilities   | 3        | 300.00 PLN   |
      | Dining Out  | 2        | 400.00 PLN   |
      | Gadgets     | 1        | 100.00 PLN   |
    When I trigger "Auto-Assign by Priority"
    Then "Rent" should receive "2,000.00 PLN"
    And "Utilities" should receive "200.00 PLN"
    And "Dining Out" should receive "0.00 PLN"
    And the unallocated pool should be reduced to "0.00 PLN"
