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

  Scenario: Blocking unsafe constructs in Lua formulas
    Given a user-submitted Lua formula attempting to use a while loop or function declaration:
      """
      while true do end
      """
    When the script is submitted for validation
    Then the engine should reject the script with a forbidden token error
    And the VM should never execute the script
