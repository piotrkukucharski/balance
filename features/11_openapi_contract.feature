Feature: OpenAPI 3.1 REST API Specification and Interfaces
  As a developer or API client integrator
  I want a complete OpenAPI 3.1 specification for all Balance endpoints
  So that I have strict Go server interfaces and a single source of truth for the REST API

  Scenario: OpenAPI contract is embedded and valid
    Given the OpenAPI specification is loaded
    Then the API title should be "Balance Personal Finance REST API"
    And the contract must declare all required endpoints from section 12.2 of the specification

  Scenario: JSON REST controllers handle registration, token generation and budget operations
    Given the Balance application database is initialized
    When I submit a REST registration request to "/api/v1/auth/register" for "api_bdd@example.com"
    Then the REST response status code should be 201
    When I submit a REST login request to "/api/v1/auth/login" for "api_bdd@example.com"
    Then the REST response status code should be 200
    When I create a personal access token via REST API
    Then the REST response status code should be 201
    When I create a budget "BDD Rest Budget" using the Bearer token
    Then the REST response status code should be 201
    And the returned budget name should be "BDD Rest Budget"

