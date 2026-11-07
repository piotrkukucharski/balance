Feature: TypeSafe Jev System One Integration in Sandboxed Lua Automations
  As a budget user
  I want to invoke Jev classification primitives (jev.choice and jev.noul) from automation scripts
  So that polymorphic transactions are classified accurately and safely without raw network access

  Scenario: Evaluating jev.choice in a Judge script for expense categorization
    Given a mock Jev service returning choice "Transport" with confidence 0.95 for question "Which category best fits this transaction?"
    And a transaction with note "UBER *TRIP 1234 HELP.UBER.COM" and amount "45.00 PLN"
    When the Lua automation judge evaluates script:
      """
      local category, conf, err = jev.choice("Which category best fits this transaction?", {"Transport", "Groceries", "Entertainment"}, {note = tx.notes})
      return err == nil and category == "Transport" and conf >= 0.8
      """
    Then the judge evaluation result should be true

  Scenario: Evaluating jev.noul in a Judge script for hypothesis testing
    Given a mock Jev service returning probability 0.92 with confidence 0.88 for question "Is this transaction a recurring monthly subscription?"
    And a transaction with note "SPOTIFY AB MONTHLY" and amount "19.99 PLN"
    When the Lua automation judge evaluates script:
      """
      local prob, conf, err = jev.noul("Is this transaction a recurring monthly subscription?", {note = tx.notes, amount = tx.amount})
      return err == nil and prob > 0.85
      """
    Then the judge evaluation result should be true

  Scenario: Rejecting loop constructs in automation scripts at lexer validation
    Given a user-submitted Lua automation script attempting to invoke Jev inside a loop:
      """
      for i = 1, 5 do
        local cat, conf, err = jev.choice("categorize", {"A", "B"}, {})
      end
      return true
      """
    When the script is submitted for validation
    Then the engine should reject the script with a forbidden token error
    And the VM should never execute the script

  Scenario: Enforcing hard quota limit of maximum 3 Jev calls per script execution
    Given a mock Jev service responding to choice queries
    And a transaction with note "AMAZON EU SARL" and amount "120.00 PLN"
    When the Lua automation judge evaluates script:
      """
      local c1, _, err1 = jev.choice("q1", {"A", "B"}, {})
      local c2, _, err2 = jev.choice("q2", {"A", "B"}, {})
      local c3, _, err3 = jev.choice("q3", {"A", "B"}, {})
      local c4, _, err4 = jev.choice("q4", {"A", "B"}, {})
      return err4 ~= nil and text.contains(err4, "quota exceeded")
      """
    Then the judge evaluation result should be true

  Scenario: Sanitizing secrets and headers on upstream Jev error responses
    Given a mock Jev service returning HTTP 500 with internal error details
    And a transaction with note "UNKNOWN PAYEE" and amount "10.00 PLN"
    When the Lua automation judge evaluates script:
      """
      local cat, conf, err = jev.choice("categorize", {"Food", "Other"}, {note = tx.notes})
      return err
      """
    Then the Lua return error should not expose authorization tokens or sensitive headers
