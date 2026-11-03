Feature: OpenAPI 3.1 REST API Specification and Interfaces
  As a developer or API client integrator
  I want a complete OpenAPI 3.1 specification for all Balance endpoints
  So that I have strict Go server interfaces and a single source of truth for the REST API

  Scenario: OpenAPI contract is embedded and valid
    Given the OpenAPI specification is loaded
    Then the API title should be "Balance Personal Finance REST API"
    And the contract must declare all required endpoints from section 12.2 of the specification

  Scenario: Interactive API documentation and raw OpenAPI specifications are publicly served
    Given the Balance application database is initialized
    When I send a GET request to "/api/docs"
    Then the REST response status code should be 200
    And the REST response body should contain "@scalar/api-reference"
    And the REST response body should contain "/api/openapi.json"
    When I send a GET request to "/api/openapi.yaml"
    Then the REST response status code should be 200
    And the REST response body should contain "openapi: 3.1.0"
    When I send a GET request to "/api/openapi.json"
    Then the REST response status code should be 200
    And the REST response JSON should have field "openapi" equal to "3.1.0"

  Scenario: JSON REST controllers handle registration, token generation, listing, revocation and budget operations
    Given the Balance application database is initialized
    When I submit a REST registration request to "/api/v1/auth/register" for "api_bdd@example.com"
    Then the REST response status code should be 201
    When I submit a REST login request to "/api/v1/auth/login" for "api_bdd@example.com"
    Then the REST response status code should be 200
    When I create a personal access token via REST API
    Then the REST response status code should be 201
    And the created personal access token should start with "bal_pat_"
    When I list personal access tokens via REST API using the Bearer token
    Then the REST response status code should be 200
    And the token list should contain "BDD PAT Token"
    When I create a budget "BDD Rest Budget" using the Bearer token
    Then the REST response status code should be 201
    And the returned budget name should be "BDD Rest Budget"
    When I revoke the personal access token via REST API using the Bearer token
    Then the REST response status code should be 204
    When I list budgets via REST API using the Bearer token
    Then the REST response status code should be 401
    And the REST error response should have code "UNAUTHORIZED"

  Scenario: Session cookie mutating REST requests require CSRF while Bearer PAT requests bypass CSRF
    Given the Balance application database is initialized
    When I submit a REST registration request to "/api/v1/auth/register" for "csrf_api@example.com"
    Then the REST response status code should be 201
    When I submit a REST login request to "/api/v1/auth/login" for "csrf_api@example.com"
    Then the REST response status code should be 200
    When I submit a REST POST to "/api/v1/budgets" with session cookie but without CSRF token
    Then the REST response status code should be 403
    And the REST error response should have code "FORBIDDEN"
    When I create a personal access token via REST API
    Then the REST response status code should be 201
    When I create a budget "CSRF Exempt Budget" using the Bearer token without CSRF header
    Then the REST response status code should be 201
    And the returned budget name should be "CSRF Exempt Budget"

  Scenario: Budget invitation workflow and session logout via REST API
    Given the Balance application database is initialized
    When I submit a REST registration request to "/api/v1/auth/register" for "inviter_api@example.com"
    And I submit a REST login request to "/api/v1/auth/login" for "inviter_api@example.com"
    And I create a personal access token via REST API
    And I create a budget "Shared API Budget" using the Bearer token
    And I invite "invitee_api@example.com" with role "EDITOR" to the budget via REST API
    Then the REST response status code should be 201
    When I submit a REST registration request to "/api/v1/auth/register" for "invitee_api@example.com"
    And I submit a REST login request to "/api/v1/auth/login" for "invitee_api@example.com"
    And I accept the budget invitation via REST API using the session cookie and CSRF token
    Then the REST response status code should be 200
    When I submit a REST logout request to "/api/v1/auth/logout" using the session cookie and CSRF token
    Then the REST response status code should be 204
    When I send a GET request to "/api/v1/users/me" with the logged-out session cookie
    Then the REST response status code should be 401
    And the REST error response should have code "UNAUTHORIZED"


