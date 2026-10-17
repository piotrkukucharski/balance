Feature: User Authentication and Stateful Browser Sessions
  As a prospective or registered user
  I want to register, authenticate securely with Argon2id, and manage my session
  So that my financial data is protected and accessible across devices

  Scenario: Successful user registration and automatic login
    Given the Balance application database is initialized
    When I submit a registration request with display name "Jan Kowalski", email "jan@example.com" and password "SuperSecret123!"
    Then the response status should be 303 Redirect to "/budgets"
    And a secure HTTP-only session cookie "balance_session" should be set
    And the session token hash in "main.db" should match the cookie token
    And a 256-bit CSRF token should be associated with the active session

  Scenario: Successful user login with valid credentials
    Given an existing registered user with email "anna@example.com" and password "Pass12345!"
    When I submit a login request with email "anna@example.com" and password "Pass12345!"
    Then the response status should be 303 Redirect to "/budgets"
    And an active session should be persisted in "sessions" table

  Scenario: User logout terminates the session
    Given an authenticated user "jan@example.com" with an active session
    When I submit a logout request with a valid CSRF token
    Then the response status should be 303 Redirect to "/login"
    And the session cookie should be invalidated
    And the session record should be deleted from "sessions" in "main.db"

  Scenario: Expired or invalid session redirects web user to login page
    Given an expired or invalid session cookie "balance_session"
    When I request a protected HTML page "/budgets"
    Then the response status should be 303 Redirect to "/login"
    And the session cookie should be invalidated

  Scenario: Expired or invalid session returns 401 for non-HTML API requests
    Given an expired or invalid session cookie "balance_session"
    When I request a protected API endpoint "/budgets" with JSON accept header
    Then the response status should be 401 Unauthorized

