Feature: External Parties, Counterparties and Auto-Matching Rules
  As a budget Editor or Owner
  I want to register counterparties and define auto-matching rules
  So that incoming bank transactions are automatically classified

  Scenario: Creating an external merchant counterparty on the fly
    Given an authenticated user "jan@example.com" on budget "Family Home"
    When I create an external party "Żabka" of type "ORGANIZATION"
    Then the party should be stored in "parties_current" with "is_user_owned = 0"
    And a virtual external account "Żabka Account" should be automatically created

  Scenario: Defining an auto-matching rule for merchant patterns
    Given an external party "Żabka" with external account "Żabka Account"
    And an existing bucket "Groceries"
    When I define a matching rule "Match Zabka" matching field "RAW_PAYEE" with operator "CONTAINS" pattern "ZABKA" to account "Żabka Account" and default bucket "Groceries"
    Then the rule should be compiled with RE2 ReDoS safety and stored in "matching_rules_current"

  Scenario: Automatically matching an incoming transaction
    Given an active matching rule for "ZABKA" mapping to "Żabka Account" and bucket "Groceries"
    When a raw transaction arrives with payee "ZABKA Z1234 WARSZAWA" and amount "-25.40 PLN"
    Then the matching engine should suggest target account "Żabka Account" and default bucket "Groceries"
