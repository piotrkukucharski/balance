Feature: Sandboxed Lua Formula Engine and AI Formula Assistant
  As a budget user
  I want to write or generate dynamic formulas for complex target relationships
  So that dependent targets automatically update without risking execution safety

  Scenario: Evaluating a dynamic mortgage overpayment formula
    Given a target "Required Mortgage Installment" with required amount "900.00 PLN"
    And a dependent target "Mortgage Overpayment" with Lua formula:
      """
      return math.max(0, money(2000) - target("Required Mortgage Installment").required)
      """
    When the Lua sandbox evaluates the target formula
    Then the result should be 110000 minor units ("1,100.00 PLN")
    And the VM instruction count should be strictly within 500 instructions
    And the formula execution should not trigger any database I/O

  Scenario: Generating a Lua formula with natural language AI Assistant
    Given an authenticated user editing target "Emergency Buffer" in bucket "Savings"
    When I request AI formula generation with prompt "Save 15% of total income over the last 30 days capped at 5000 PLN"
    Then the AI assistant should generate valid sandboxed Lua code:
      """
      return math.min(money(5000), math.floor(budget.income(30, "PLN") * 0.15))
      """
    And the sandbox dry-run should evaluate without errors
    And a live calculated preview badge should be displayed in the UI

  Scenario: Generating a Lua formula for periodic expense with AI Assistant
    Given an authenticated user editing target "Karta Miejska" in bucket "Transport"
    When I request AI formula generation with prompt "125 zł płatne co trzy miesiące"
    Then the AI assistant should generate valid sandboxed Lua code:
      """
      return math.ceil(money(125) * window.days / 90)
      """
    And the sandbox dry-run should evaluate without errors
    And a live calculated preview badge should be displayed in the UI

  Scenario: Generating a Lua formula for loan installment using financial.pmt and AI Assistant
    Given an authenticated user editing target "Hipoteka" with anchor due date "2036-10-25" in bucket "Housing"
    When I request AI formula generation with prompt "Oblicz ratę kredytu 300000 zł przy oprocentowaniu 6% rocznie"
    Then the AI assistant should generate valid sandboxed Lua code:
      """
      local nper = date.diff_months(self.anchor_due_date, runtime.today)
      local r = 0.06 / 12
      local pv = 300000
      local pmt = -financial.pmt(r, nper, pv)
      return money(pmt)
      """
    And the sandbox dry-run should evaluate without errors
    And a live calculated preview badge should be displayed in the UI

  Scenario: Blocking unsafe constructs in Lua formulas
    Given a user-submitted Lua formula attempting to use a while loop or function declaration:
      """
      while true do end
      """
    When the script is submitted for validation
    Then the engine should reject the script with a forbidden token error
    And the VM should never execute the script

  Scenario: Editing an existing target and switching to Lua formula mode
    Given an open budget "Family Home"
    And an existing bucket "Housing" in group "Fixed"
    And an existing target "Rent" in bucket "Housing" with required amount "2,000.00 PLN"
    When I edit target "Rent" switching amount mode to "LUA_FORMULA" with script:
      """
      return money(2100)
      """
    Then the target in projections should have amount mode "LUA_FORMULA" and script:
      """
      return money(2100)
      """
    And the target requirement in projections should be "2,100.00 PLN"

  Scenario: Calculating loan installment using financial.pmt with self.anchor_due_date and runtime.today
    Given a target "Hipoteka" with anchor due date "2036-10-25" and Lua formula:
      """
      local nper = date.diff_months(self.anchor_due_date, runtime.today)
      local r = 0.06 / 12
      local pv = 300000
      local pmt = -financial.pmt(r, nper, pv)
      return money(pmt)
      """
    When the Lua sandbox evaluates the target formula with simulated today "2026-10-25"
    Then the result should be 333062 minor units ("3,330.62 PLN")
