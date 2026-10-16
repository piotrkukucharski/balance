Feature: Progressive Web App (PWA) Online-Only Compliance
  As a mobile or desktop user
  I want to install Balance as a PWA
  So that I have an app-like standalone experience while maintaining server-only financial integrity

  Scenario: Serving Web App Manifest for installation
    When I request "GET /manifest.webmanifest"
    Then the response status should be 200 OK
    And the content type should be "application/manifest+json"
    And the manifest should specify "display: standalone" and start URL "/budgets"

  Scenario: Serving Pass-Through NetworkOnly Service Worker
    When I request "GET /sw.js"
    Then the response status should be 200 OK
    And the content type should be "application/javascript"
    And the script should implement a "NetworkOnly" fetch strategy
    And the script should contain a connection-required fallback without caching financial data
