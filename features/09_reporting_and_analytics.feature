Feature: Comprehensive Reporting, Age of Money and Monte Carlo Simulation
  As a budget user
  I want interactive charts, cashflow metrics, and retirement longevity simulations
  So that I have deep visibility into my financial health and future runway

  Scenario: Calculating Age of Money via chronological FIFO queue
    Given the following external liquid inflows into user accounts:
      | Day    | Amount       |
      | Day 1  | 1,000.00 PLN |
    And the following external liquid outflows:
      | Day    | Amount     | Expected Age |
      | Day 11 | 400.00 PLN | 10 days      |
      | Day 21 | 600.00 PLN | 20 days      |
    When the Age of Money engine evaluates the transactions
    Then the weighted Age of Money headline metric should be 15 days
    And the historical time-series should record both depletion points

  Scenario: Running a bounded Monte Carlo longevity simulation
    Given an investment retirement pot of "1,000,000.00 PLN" accessible from age 60
    And an annual spending phase of "50,000.00 PLN" from age 60 to 65
    When I run a Monte Carlo simulation with 1,000 parallel iterations
    Then the simulation should complete within 2 seconds
    And the success rate percentage should be greater than 90%
    And 10th, 50th (median), and 90th percentile trajectories should be returned

  Scenario: Viewing the 10 ECharts interactive reports dashboard
    Given an authenticated user on budget "Family Home"
    When I navigate to "/budgets/:budgetId/reports"
    Then the reports dashboard should render 10 report cards:
      | Report Card            |
      | Age of Money           |
      | Income v. Expense      |
      | Spending Trends        |
      | Spending Breakdown     |
      | Net Worth              |
      | Sankey Flow            |
      | Balance Forecast       |
      | Budget Analysis        |
      | Monte Carlo Simulator  |
      | Counterparty Cashflow  |

  Scenario: Viewing dedicated full-page detailed report
    Given an authenticated user on budget "Family Home"
    When I navigate to "/budgets/:budgetId/reports/income-expense"
    Then the detailed report page should display title "Income vs. Expense"
    And the page should render filter controls for date range, currency, and internal transfers
    And the full-screen report chart container should be present

  Scenario: Saving and managing custom report configurations
    Given an authenticated user on budget "Family Home"
    When I save a report configuration named "My Custom Spending" for report "SPENDING_BREAKDOWN" with pinned status
    Then the saved report should be persisted in "saved_reports_current"
    And the saved report configuration should be listed in the reports dashboard
    When I toggle the pinned widget status of "My Custom Spending"
    Then the saved report widget should be updated with pinned status "0"
    When I delete the saved report "My Custom Spending"
    Then the saved report should be removed from "saved_reports_current"

  Scenario: Caching report results with daily validity and on-demand recalculation
    Given an authenticated user on budget "Family Home"
    When I request data for report "age-of-money"
    Then the response header "X-Report-Cached" should be "false"
    And the report result should be cached in "report_cache"
    When I request data for report "age-of-money" again
    Then the response header "X-Report-Cached" should be "true"
    When I request data for report "age-of-money" with force recalculation
    Then the response header "X-Report-Cached" should be "false"

