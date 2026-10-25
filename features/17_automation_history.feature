Feature: Lua Automation Version History, Diffs, Restore, and Multi-Version Squash
  As a budget user and manager
  I want to inspect chronological version history, compare diffs of Lua scripts, restore previous versions, and squash version ranges
  So that automation scripts can be safely developed, audited, and cleaned up

  Scenario: Viewing automation version timeline, Lua scripts, and diffs in the history drawer
    Given an open budget "Family Home"
    And an active automation rule "Auto Categorize Uber" with priority 10:
      | judge    | text.contains(tx.notes, "Uber") |
      | executor | tx.set_bucket("Transport")      |
      | comment  | Initial rule for Uber rides     |
    When I update the automation rule "Auto Categorize Uber" with comment "Added bolt":
      | judge    | text.contains(tx.notes, "Uber") or text.contains(tx.notes, "Bolt") |
      | executor | tx.set_bucket("Transport")                                         |
    When I request the history drawer for automation "Auto Categorize Uber"
    Then the response status should be 200 OK
    And the history drawer should display version timeline in descending order
    And the history drawer should display the diff for "judge_script"
    And the history drawer should display the action badges and restore buttons

  Scenario: Restoring an automation to a previous version creates a new version with action RESTORE
    Given an open budget "Family Home"
    And an active automation rule "Auto Categorize Uber" with priority 10:
      | judge    | text.contains(tx.notes, "Uber") |
      | executor | tx.set_bucket("Transport")      |
      | comment  | Initial rule for Uber rides     |
    When I update the automation rule "Auto Categorize Uber" with comment "Broken rule":
      | judge    | text.contains(tx.notes, "Broken") |
      | executor | tx.set_bucket("Unknown")          |
    When I submit a restore request for automation "Auto Categorize Uber" to version 1
    Then the response status should be 200 OK
    And the automation rule "Auto Categorize Uber" should be at version 3
    And the current judge script for "Auto Categorize Uber" should be 'text.contains(tx.notes, "Uber")'
    And the latest action in "automations" should be "RESTORE"

  Scenario: Multi-version squash collapses selected versions into a single version
    Given an open budget "Family Home"
    And an active automation rule "Auto Categorize Uber" with priority 10:
      | judge    | text.contains(tx.notes, "Uber") |
      | executor | tx.set_bucket("Transport")      |
      | comment  | v1 initial                      |
    When I update the automation rule "Auto Categorize Uber" with comment "v2 tweak":
      | judge    | text.contains(tx.notes, "Uber") or tx.amount > 10 |
      | executor | tx.set_bucket("Transport")                        |
    And I update the automation rule "Auto Categorize Uber" with comment "v3 tweak":
      | judge    | text.contains(tx.notes, "Uber") or tx.amount > 20 |
      | executor | tx.set_bucket("Transport")                        |
    And I update the automation rule "Auto Categorize Uber" with comment "v4 final":
      | judge    | text.contains(tx.notes, "Uber") or tx.amount > 50 |
      | executor | tx.set_bucket("Transport")                        |
    When I submit a multi-version squash for automation "Auto Categorize Uber" from version 2 to 4 with comment "Squashed experimental tweaks v2-v4"
    Then the response status should be 200 OK
    And the automation rule "Auto Categorize Uber" should have exactly 2 versions in audit history
    And the automation rule "Auto Categorize Uber" should be at version 4
    And the latest version should have comment "Squashed experimental tweaks v2-v4"

  Scenario: Rejecting invalid squash ranges
    Given an open budget "Family Home"
    And an active automation rule "Auto Categorize Uber" with priority 10:
      | judge    | text.contains(tx.notes, "Uber") |
      | executor | tx.set_bucket("Transport")      |
      | comment  | v1 initial                      |
    When I submit an invalid multi-version squash from version 3 to 1
    Then the action should be rejected with status 400 Bad Request

  Scenario: Loading historical version into editor without saving
    Given an open budget "Family Home"
    And an active automation rule "Auto Categorize Uber" with priority 10:
      | judge    | text.contains(tx.notes, "Uber") |
      | executor | tx.set_bucket("Transport")      |
      | comment  | v1 initial                      |
    When I fetch historical version 1 for automation "Auto Categorize Uber"
    Then the response status should be 200 OK
    And the JSON response should have judge_script 'text.contains(tx.notes, "Uber")' and version 1
