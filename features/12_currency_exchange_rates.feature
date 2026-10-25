Feature: Central Bank Exchange Rates and Multi-Currency Conversion
  As a multi-currency budget user
  I want accurate, automated foreign exchange rates from the European Central Bank and National Bank of Poland
  So that cross-currency transactions, ledger balances, and reports reflect authoritative market values

  Scenario: Synchronizing exchange rates from ECB and NBP feeds
    Given the exchange rate storage in "main.db" is empty
    When I trigger central bank synchronization for 90 days of history
    Then exchange rates from "ECB" should be persisted with base currency "EUR"
    And exchange rates from "NBP" should be persisted with target currency "PLN"
    And each persisted record should retain its source and date without inverse duplicates in the database

  Scenario: Global priority of ECB over NBP for overlapping currency pairs
    Given an ECB published rate for "EUR" to "PLN" on "2026-10-23" is "4.300000"
    And an NBP published rate for "EUR" to "PLN" on "2026-10-23" is "4.320000"
    When I query the effective exchange rate for "EUR" to "PLN" on "2026-10-23"
    Then the resolved rate should be "4.300000" with source "ECB"

  Scenario: Dynamic inversion and cross-rate triangulation without stored inverse pairs
    Given a persisted ECB exchange rate for "EUR" to "USD" on "2026-10-23" is "1.080000"
    And a persisted ECB exchange rate for "EUR" to "PLN" on "2026-10-23" is "4.320000"
    When I convert "100.00 USD" to "EUR" on "2026-10-23"
    Then the converted amount should be dynamically inverted to "92.59 EUR"
    When I convert "100.00 USD" to "PLN" on "2026-10-23"
    Then the converted amount should be triangulated via "EUR" to "400.00 PLN"

  Scenario: Linear interpolation for weekend transactions between published business days
    Given a Friday "2026-10-16" rate for "EUR" to "PLN" is "4.300000"
    And a Monday "2026-10-19" rate for "EUR" to "PLN" is "4.330000"
    When I convert "100.00 EUR" to "PLN" on Saturday "2026-10-17"
    Then the linearly interpolated rate should be "4.310000" and result in "431.00 PLN"
    When I convert "100.00 EUR" to "PLN" on Sunday "2026-10-18"
    Then the linearly interpolated rate should be "4.320000" and result in "432.00 PLN"

  Scenario: In-progress weekend uses latest known Friday rate before Monday publication
    Given a Friday "2026-10-23" rate for "EUR" to "PLN" is "4.300000"
    And Monday "2026-10-26" rate is not yet published
    When I convert "100.00 EUR" to "PLN" on Saturday "2026-10-24"
    Then the rate should fall back to Friday rate "4.300000" resulting in "430.00 PLN"

  Scenario: Strict error when requesting rates older than available history
    Given exchange rates history is only available starting from "2026-07-27"
    When I attempt to convert "100.00 EUR" to "PLN" on "2026-01-15"
    Then the conversion should fail with an exchange rate not found error

  Scenario: Triangulated conversion fails fast without stack overflow when exchange rate history is missing
    Given the exchange rate storage in "main.db" is empty
    When I attempt to convert "100.00 GBP" to "PLN" on "2026-10-25"
    Then the conversion should fail with an exchange rate not found error
    When I attempt to convert "100.00 GBP" to "USD" on "2026-10-25"
    Then the conversion should fail with an exchange rate not found error

  Scenario: Manual exchange rate synchronization via REST API
    Given an authenticated API user
    When I send a POST request to "/api/v1/exchange-rates/sync"
    Then the response status should be 200 OK
    And the response body should contain "synced_count" greater than 0
