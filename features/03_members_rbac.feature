Feature: Budget Members, Invitations and RBAC Hierarchy
  As a budget Owner or Admin
  I want to invite collaborators and enforce permission roles
  So that editors can budget and viewers have strictly read-only access

  Scenario: Inviting a new editor via token-based invitation
    Given an authenticated user "jan@example.com" with "OWNER" role on budget "Family Home"
    When I invite "marek@example.com" as an "EDITOR"
    Then a new invitation should be created in "budget_invitations" in "main.db"
    And the invitation should have a secure 256-bit CSPRNG token hash and 7-day expiration
    And the user "marek@example.com" should receive an invitation link

  Scenario: Accepting an invitation grants budget membership
    Given a pending budget invitation for "marek@example.com" with role "EDITOR"
    When "marek@example.com" accepts the invitation token
    Then "marek@example.com" should be added to "budget_members" with role "EDITOR"
    And the invitation record should be marked with "accepted_at" timestamp
    And "marek@example.com" should now be authorized to open "Family Home"

  Scenario: Enforcing RBAC permissions on mutating actions
    Given an authenticated user "kasia@example.com" with "VIEWER" role on budget "Family Home"
    When "kasia@example.com" attempts to create a manual transaction
    Then the action should be rejected with status 403 Forbidden
