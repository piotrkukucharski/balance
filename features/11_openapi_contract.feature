Feature: OpenAPI 3.1 REST API Specification and Interfaces
  As a developer or API client integrator
  I want a complete OpenAPI 3.1 specification for all Balance endpoints
  So that I have strict Go server interfaces and a single source of truth for the REST API

  Scenario: OpenAPI contract is embedded and valid
    Given the OpenAPI specification is loaded
    Then the API title should be "Balance Personal Finance REST API"
    And the contract must declare all required endpoints from section 12.2 of the specification
