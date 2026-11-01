Feature: Internationalization and Localization Preferences (i18n)
  As a user of the Balance personal finance system
  I want to use the interface in my preferred language (such as English or Polish)
  And format numbers, dates, times, and currencies according to regional standards or custom choices
  So that my financial data is presented clearly and intuitively

  Scenario: User customizes language and format preferences in profile
    Given an authenticated user "maria@example.com" with an active session
    When I update my profile settings with display name "Maria Nowak", locale "pl-PL", date format "DD.MM.YYYY", time format "24h" and number format "1 000,99"
    Then the profile preferences should be persisted in the "users" table with locale "pl-PL"
    And navigating to "/settings/profile" should display interface elements localized in Polish
