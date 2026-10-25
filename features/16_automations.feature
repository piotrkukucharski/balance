Feature: Lua Transaction Automations Engine with Judge, Executor, AI Assistant, and Versioning
  As a budget user
  I want to define Lua automation rules with Judge conditions and Executor actions
  So that incoming transactions are automatically categorized and handled safely

  Scenario: Creating and viewing an automation rule with Judge and Executor
    Given an open budget "Family Home"
    When I create an automation rule "Auto Categorize Uber" with priority 10:
      | judge    | text.contains(tx.notes, "Uber") |
      | executor | tx.set_bucket("Transport")      |
      | comment  | Initial rule for Uber rides     |
    Then the automation rule "Auto Categorize Uber" should be active with version 1
    And visiting the automations page should display "Auto Categorize Uber"

  Scenario: Automatic in-pipeline execution upon transaction creation
    Given an open budget "Family Home"
    And an existing bucket "Transport" in group "Fixed"
    And an active automation rule "Auto Categorize Uber" with priority 10:
      | judge    | text.contains(tx.notes, "Uber") |
      | executor | tx.set_bucket("Transport")      |
      | comment  | Transport auto-rule             |
    When I create a manual transaction with amount "45.00 PLN", date "2026-10-25", and note "Uber ride to airport"
    Then the recorded transaction should automatically have bucket "Transport" assigned

  Scenario: Dry-run evaluation on a selected transaction
    Given an open budget "Family Home"
    And an existing bucket "Transport" in group "Fixed"
    And an existing manual transaction with amount "30.00 PLN", date "2026-10-25", and note "Uber ride home"
    When I test a dry run with judge 'text.contains(tx.notes, "Uber")' and executor 'tx.set_bucket("Transport")' on that transaction
    Then the dry-run outcome should indicate matched true with bucket "Transport"

  Scenario: Batch execution of active automations across transactions
    Given an open budget "Family Home"
    And an existing bucket "Transport" in group "Fixed"
    And an existing manual transaction with amount "25.00 PLN", date "2026-10-25", and note "Uber trip 1"
    And an existing manual transaction with amount "35.00 PLN", date "2026-10-25", and note "Uber trip 2"
    When I create an automation rule "Auto Categorize Uber" with priority 10:
      | judge    | text.contains(tx.notes, "Uber") |
      | executor | tx.set_bucket("Transport")      |
      | comment  | Uber batch rule                 |
    And I execute batch automations across all transactions
    Then all Uber transactions should have bucket "Transport" assigned

  Scenario: Updating an automation rule and performing a squash
    Given an open budget "Family Home"
    And an existing bucket "Transport" in group "Fixed"
    And an active automation rule "Auto Transport" with priority 10:
      | judge    | text.contains(tx.notes, "Taxi") |
      | executor | tx.set_bucket("Transport")      |
      | comment  | v1 Taxi rule                    |
    When I update the automation rule "Auto Transport" with comment "v2 Taxi rule":
      | judge    | text.contains(tx.notes, "Taxi") or text.contains(tx.notes, "Bolt") |
      | executor | tx.set_bucket("Transport")                                         |
    Then the automation rule "Auto Transport" should be at version 2
    When I update the automation rule "Auto Transport" with squash enabled and comment "v2 squashed":
      | judge    | text.contains(tx.notes, "Taxi") or text.contains(tx.notes, "Bolt") or text.contains(tx.notes, "Uber") |
      | executor | tx.set_bucket("Transport")                                                                              |
    Then the automation rule "Auto Transport" should have exactly 1 version in audit history
    And the latest version should have comment "v2 squashed"
