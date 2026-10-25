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

  Scenario: Login with non-existent user displays error message on login page
    Given the Balance application database is initialized
    When I submit a login request with email "nonexistent@example.com" and password "WrongPassword123!"
    Then the login page should be rendered with an error message to try again

  Scenario: Authenticating via valid Bearer Personal Access Token (bal_pat_...)
    Given an existing registered user with email "bearer_user@example.com" and password "Pass12345!"
    And an active Personal Access Token named "CLI Token" for user "bearer_user@example.com"
    When I request a protected API endpoint "/budgets" with the Bearer token and JSON accept header
    Then the response status should be 200 OK
    And the token last_used_at in "api_tokens" should be updated asynchronously

  Scenario: Rejecting revoked or expired Bearer Personal Access Token
    Given an existing registered user with email "revoked_user@example.com" and password "Pass12345!"
    And a revoked Personal Access Token named "Old Token" for user "revoked_user@example.com"
    When I request a protected API endpoint "/budgets" with the Bearer token and JSON accept header
    Then the response status should be 401 Unauthorized

  Scenario: Customizing user date and time format preferences in profile
    Given an existing registered user with email "format_user@example.com" and password "Pass12345!"
    When I submit a login request with email "format_user@example.com" and password "Pass12345!"
    And I navigate to user profile preferences
    Then the profile page should display date format options including "YYYY-MM-DD", "DD.MM.YYYY", "DD/MM/YYYY", "MM/DD/YYYY"
    And the profile page should display time format options including "24h" and "12h"
    When I update my date format to "DD.MM.YYYY" and time format to "12h"
    Then the response status should be 303 Redirect to "/settings/profile?saved=true"
    And my user profile in "main.db" should store date format "DD.MM.YYYY" and time format "12h"
    And subsequent HTML pages should render "data-date-format" as "DD.MM.YYYY" and "data-time-format" as "12h"

  Scenario: Disabling user registration blocks registration endpoint and hides registration links
    Given user registration is disabled via environment variable "ALLOW_REGISTRATION=false"
    When I navigate to the login page
    Then the login page should not display a link to create an account
    And the login page should inform that registration is disabled
    When I request the registration page "/register"
    Then the response status should be 403 Forbidden
    When I submit a registration request with display name "Blocked User", email "blocked@example.com" and password "Pass12345!"
    Then the response status should be 403 Forbidden
    And user "blocked@example.com" should not exist in "main.db"





