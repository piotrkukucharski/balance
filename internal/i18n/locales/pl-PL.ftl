# Balance Fluent Translations (pl-PL)
# Default locale metadata
locale-name = Polski (PL)
locale-number-grouping = {" "}
locale-number-decimal = ,
locale-date-format = DD.MM.YYYY
locale-time-format = 24h
locale-currency-pattern = { $amount } { $currency }

# Global / Navigation
app-name = Balance
nav-budget = 📂 1. Budżet (Koperty)
nav-accounts = 💳 2. Konta
nav-transactions = 💸 3. Transakcje
nav-automations = ⚡ 4. Automatyzacje
nav-reports = 📊 5. Raporty
nav-parties = 🏢 6. Kontrahenci
nav-settings = ⚙️ 7. Ustawienia
nav-settings-general = Ogólne
nav-settings-members = 👥 Członkowie
nav-settings-tokens = 🔑 Tokeny API
nav-settings-profile = 👤 Profil i Preferencje
nav-logout = Wyloguj się
nav-back = ← Wstecz

# Bottom Dock (Mobile)
dock-budget = Budżet
dock-accounts = Konta
dock-transactions = Transakcje
dock-automations = Reguły
dock-reports = Raporty
dock-settings = Ustawienia

# Auth (Login / Register)
auth-login-title = Witaj w Balance
auth-login-subtitle = Zaloguj się, aby zarządzać swoim budżetem i kopertami.
auth-credentials = DANE LOGOWANIA
auth-email = Adres e-mail
auth-password = Hasło
auth-signin-btn = Zaloguj się
auth-no-account = Nie masz jeszcze konta?
auth-create-one = Załóż konto tutaj
auth-registration-disabled = Rejestracja publiczna jest obecnie wyłączona przez administratora.
auth-register-title = Utwórz nowe konto
auth-register-subtitle = Budżetowanie kopertowe z pełną przejrzystością wielowalutową.
auth-enter-details = WPROWADŹ DANE
auth-fullname = Imię i nazwisko / Nazwa wyświetlana
auth-get-started-btn = Rozpocznij za darmo
auth-already-account = Masz już konto?
auth-login-here = Zaloguj się tutaj
auth-invalid-credentials = Nieprawidłowy e-mail lub hasło. Spróbuj ponownie.
auth-registration-disabled-err = Rejestracja użytkowników jest obecnie wyłączona.

# Profile & Preferences View
profile-title = Profil i Preferencje
profile-desc = Zarządzaj danymi konta, językiem oraz preferencjami formatowania dat, czasu i liczb.
profile-saved-success = ✓ Preferencje zostały pomyślnie zaktualizowane.
profile-section-personal = Dane osobowe
profile-section-localization = Język i Formaty
profile-label-display-name = Nazwa wyświetlana
profile-label-email = Adres e-mail
profile-label-language = Język interfejsu
profile-label-date-format = Format daty
profile-label-time-format = Format czasu
profile-label-number-format = Format liczb i kwot
profile-option-auto-default = Domyślny dla języka (automatycznie)
profile-live-preview = Podgląd na żywo
profile-preview-date = Podgląd daty:
profile-preview-timestamp = Podgląd znacznika czasu:
profile-preview-number = Podgląd liczb:
profile-preview-currency = Podgląd waluty:
profile-btn-save = Zapisz preferencje

# Common Actions & Badges
action-save = Zapisz
action-cancel = Anuluj
action-delete = Usuń
action-edit = Edytuj
action-confirm = Potwierdź
action-close = Zamknij
action-add = Dodaj
action-rename = Zmień nazwę
action-clone = Klonuj
action-restore = Przywróć
action-search = Szukaj
action-filter = Filtruj
action-refresh = Odśwież
action-sync = Synchronizuj
badge-active = AKTYWNY
badge-deleted = USUNIĘTY

# Budgets List View
budgets-list-title = Twoje Budżety
budgets-list-desc = Wybierz aktywny budżet lub utwórz nowy obszar kopertowy.
budgets-list-new-btn = Nowy Budżet
budgets-list-clone-btn = Klonuj budżet
budgets-list-delete-btn = Usuń budżet
budgets-list-timezone = Strefa czasowa: { $tz }
budgets-list-no-budgets = Nie masz jeszcze żadnych utworzonych budżetów.
budgets-list-default-currency = Waluta podstawowa
budgets-list-open-btn = Otwórz
budgets-list-link-bank-btn = Połącz z bankiem

# Budget (Buckets) View
budget-title = Budżet
budget-age-of-money = Wiek Pieniądza: { $days } dni
budget-rta-title = Gotowe do przydziału
budget-rta-assigned-desc = Wszystkie środki przydzielone do kopert
budget-needed-title = Potrzebne ({ $horizon })
budget-mandatory-needed = w tym Obowiązkowe:
budget-all-mandatory-funded = Wszystkie obowiązkowe cele sfinansowane
budget-auto-assign-btn = Automatyczny przydział wg priorytetów
budget-filter-all = Wszystkie
budget-filter-attention = Wymagające uwagi
budget-filter-overspent = Przekroczone
budget-filter-buffer = Z buforem
budget-expand-all = Rozwiń wszystkie
budget-collapse-all = Zwiń wszystkie
budget-search-placeholder = Szukaj kopert...
budget-new-bucket-btn = Nowa Koperta
budget-accounts-summary = Podsumowanie Kont
budget-no-accounts = Brak połączonych kont.
budget-group-delete-btn = Usuń kategorię
budget-group-delete-confirm = Czy na pewno chcesz usunąć kategorię '{ $name }'?

# Budget Settings View
settings-title = Ustawienia Budżetu
settings-desc = Skonfiguruj preferencje obszaru roboczego, domyślną strefę czasową i parametry księgi.
settings-general-card = Informacje ogólne
settings-budget-name = Nazwa budżetu
settings-default-currency = Domyślna waluta
settings-currency-immutable = Główna waluta jest niezmienna po utworzeniu budżetu
settings-timezone = Strefa czasowa IANA
settings-save-btn = Zapisz ustawienia
settings-role-notice = Przeglądasz ustawienia budżetu z uprawnieniami { $role }. Tylko WŁAŚCICIEL (OWNER) może modyfikować parametry budżetu.

# Time Horizons Card
settings-horizons-card = Horyzonty Czasowe
settings-horizons-desc = Konfiguruj kroczące i kalendarzowe okna kalkulacji. Każdy członek może wybrać swój domyślny widok.
settings-horizons-user-default = Twój Domyślny Horyzont
settings-horizons-user-default-desc = Horyzont aktywowany automatycznie po wejściu do tego budżetu.
settings-horizons-save-pref-btn = Zapisz domyślny horyzont
settings-horizons-table-title = Dostępne Horyzonty
settings-horizons-col-horizon = Horyzont
settings-horizons-col-type = Typ
settings-horizons-col-status = Status
settings-horizons-col-actions = Akcje
settings-horizons-type-monthly = Miesiąc kalendarzowy
settings-horizons-type-annual = Rok kalendarzowy
settings-horizons-type-rolling = Okno kroczące
settings-horizons-type-calendar-day = Dzień miesiąca
settings-horizons-status-default = Twój domyślny
settings-horizons-status-visible = Widoczny
settings-horizons-status-hidden = Ukryty
settings-horizons-btn-hide = Ukryj
settings-horizons-btn-show = Pokaż
settings-horizons-btn-delete = Usuń
settings-horizons-add-title = Dodaj Nowy Horyzont
settings-horizons-add-type = Typ horyzontu
settings-horizons-opt-rolling = Dni kroczące (1-3650)
settings-horizons-opt-day-of-month = Dzień miesiąca (1-28)
settings-horizons-label-days = Liczba dni
settings-horizons-label-day-num = Dzień miesiąca
settings-horizons-btn-add = Dodaj horyzont
settings-horizons-modal-title = Przypisz użytkowników przed ukryciem/usunięciem
settings-horizons-modal-desc = Horyzont, który chcesz ukryć lub usunąć, jest obecnie ustawiony jako domyślny dla następujących członków budżetu:
settings-horizons-modal-select-fallback = Wybierz alternatywny widoczny horyzont dla tych osób:
settings-horizons-modal-confirm-btn = Potwierdź i przepisz
settings-horizons-owner-only-notice = Tylko WŁAŚCICIEL (OWNER) budżetu może dodawać, ukrywać lub usuwać horyzonty czasowe.

# Horizon Labels
horizon-name-monthly = Miesięczny
horizon-name-annual = Roczny
horizon-name-rolling = { $days }d
horizon-name-calendar-toolbar = Do { $day }.
horizon-name-calendar-full = Do { $day }. dnia miesiąca

settings-personal-pref-title = Preferencje osobiste
settings-personal-pref-desc = Skonfiguruj własny format daty ({ $dateFormat }) oraz czasu ({ $timeFormat }).
settings-personal-pref-btn = Dostosuj formaty →
settings-debug-card-title = Narzędzia diagnostyczne (Debug)
settings-debug-card-desc = Sprawdź integralność finansową, osierocone salda i wykonaj akcje naprawcze bilansu.
settings-debug-card-btn = Otwórz Narzędzia Debug →
settings-danger-zone = Strefa Niebezpieczna
settings-danger-zone-desc = Trwale usuń ten budżet, koperty, zapisy księgi, transakcje oraz historię kont. Tej operacji nie można cofnąć.
settings-delete-budget-btn = Usuń Budżet
settings-delete-confirm = Czy na pewno chcesz usunąć ten budżet? Wszystkie dane zostaną bezpowrotnie skasowane.
settings-danger-owner-only = Tylko WŁAŚCICIEL (OWNER) budżetu może usunąć ten obszar roboczy.

# Diagnostic Tools (Debug) View
debug-title = Narzędzia diagnostyczne (Debug)
debug-desc = Weryfikuj spójność księgi w czasie rzeczywistym, wykrywaj osierocone rekordy i uruchamiaj naprawcze procedury transakcyjne.
debug-back-to-settings = ← Wróć do Ustawień
debug-badge = Narzędzia diagnostyczne (Debug)
debug-toast-success = Operacja zakończona pomyślnie
debug-integrations-title = Integracje (AI & GoCardless)
debug-integrations-desc = Weryfikacja autoryzacji, łączności i statusu zewnętrznych dostawców usług.
debug-gemini-title = Google Gemini API
debug-gemini-status-untested = Nie sprawdzono
debug-gemini-status-checking = Sprawdzanie...
debug-gemini-status-ok = Działa poprawnie
debug-gemini-desc = Weryfikacja komunikacji z modelem LLM na teście promptu "2 + 2 =".
debug-gemini-btn = Audyt AI
debug-gocardless-title = GoCardless (Open Banking)
debug-gocardless-status-untested = Nie sprawdzono
debug-gocardless-status-checking = Sprawdzanie...
debug-gocardless-status-ok = Działa poprawnie
debug-gocardless-desc = Weryfikacja autoryzacji tokenu OAuth i pobierania instytucji bankowych z API.
debug-gocardless-btn = Audyt GoCardless
debug-integrations-status-error = Błąd ({ $status })
debug-integrations-status-failure = Awaria
debug-integrations-http-status = Status HTTP: { $status }
debug-integrations-error = Błąd: { $error }
debug-integrations-api-response = Odpowiedź API:
debug-integrations-request-failed = Błąd wykonania żądania: { $error }
debug-ai-success = Połączenie z Google Gemini API ({ $model }) działa poprawnie. Odpowiedź na '2 + 2 =': { $reply }
debug-gocardless-success = Połączenie z GoCardless API poprawne. Pobrano { $count } instytucji bankowych dla kraju { $country }.
debug-gocardless-no-keys = Brak skonfigurowanych kluczy GOCARDLESS_SECRET_ID / GOCARDLESS_SECRET_KEY w środowisku
debug-gocardless-api-error = Błąd komunikacji z GoCardless API: { $error }

# Debug Audit Card
debug-audit-card-title = Ledger Spójność & Integralność Bilansowa
debug-audit-card-desc = Weryfikacja spójności w czasie rzeczywistym wykonywana w izolowanej transakcji.
debug-audit-all-passed = Wszystkie testy księgi zaliczone
debug-audit-anomalies-detected = Wykryto anomalie bilansowe
debug-audit-check1-title = 1. Osierocone Salda & Transakcje (Konta usunięte z accounts_current)
debug-audit-check1-clean = Brak osieroconych sald ani transakcji powiązanych z nieistniejącymi kontami.
debug-audit-orphaned-balances-alert = Wykryto { $count } osieroconych rekordów sald w account_currency_balances!
debug-audit-orphaned-accounts-alert = Wykryto { $count } nieistniejących kont powiązanych z transakcjami lub saldami:
debug-audit-orphaned-txs-details = Szczegóły osieroconych transakcji ({ $count }):
debug-audit-col-account-id = ID Konta
debug-audit-col-currency = Waluta
debug-audit-col-amount = Kwota
debug-audit-col-account-name = Nazwa konta (Historia)
debug-audit-col-budget-txs = Transakcje budżetowe
debug-audit-col-raw-txs = Transakcje surowe (Raw)
debug-audit-col-history-status = Status w historii
debug-audit-col-actions = Akcje naprawcze
debug-audit-col-type = Typ
debug-audit-col-roles = Rola brakującego konta
debug-audit-col-missing-acct = ID brakującego konta
debug-audit-col-notes-payee = Notatki / Odbiorca
debug-audit-btn-resurrect = Wskrześ Konto
debug-audit-not-in-history = Brak w historii
debug-audit-confirm-resurrect = Przywrócić konto "{ $name }" w stanie zamkniętym (is_closed=1)?
debug-audit-check2-title = 2. Zgodność Transakcji z Saldami Kont
debug-audit-check2-clean = Wszystkie salda kont dokładnie odpowiadają sumie transakcji budżetowych.
debug-audit-check2-discrepancies-alert = Wykryto { $count } kont z rozbieżnością między saldem zapisanym a historią transakcji!
debug-audit-col-stored = Zapisane
debug-audit-col-computed = Wyliczone
debug-audit-col-diff = Różnica
debug-audit-check3-title = 3. Równanie Fundamentalne (Gotówka == Koperty + RTA)
debug-audit-status-balanced = Zbilansowane
debug-audit-status-unbalanced = Niezbilansowane
debug-audit-btn-rerun = Uruchom Audyt Ponownie
debug-audit-btn-cleanup-orphaned = Usuń Osierocone Salda
debug-audit-confirm-cleanup-orphaned = Czy na pewno chcesz usunąć osierocone rekordy sald?
debug-audit-btn-delete-orphaned-txs = Usuń Osierocone Transakcje
debug-audit-confirm-delete-orphaned-txs = Czy na pewno chcesz trwale usunąć wszystkie osierocone transakcje budżetowe i raw?
debug-audit-btn-reconcile = Generuj Korektę Bilansową
debug-audit-confirm-reconcile = Utworzyć automatyczne transakcje korygujące dla wykrytych rozbieżności sald?
debug-notify-audit-clean = Audyt integralności zakończony sukcesem.
debug-notify-audit-anomalies = Audyt zakończony: wykryto anomalie bilansowe.
debug-notify-cleanup-balances-success = Wyczyszczono { $count } osieroconych rekordów sald.
debug-notify-resurrect-success = Konto "{ $name }" zostało pomyślnie wskrzeszone jako zamknięte (is_closed=1).
debug-notify-resurrect-not-found = Konto nie istnieje w historii accounts
debug-notify-delete-txs-success = Pomyślnie usunięto { $bCount } transakcji budżetowych i { $rCount } transakcji raw.
debug-notify-reconcile-success = Wygenerowano { $count } transakcji korygujących (Reconciliation Adjustment).
debug-notify-reconcile-none = Brak rozbieżności sald do skorygowania.

# Members View
members-title = Członkowie i Uprawnienia
members-desc = Hierarchia uprawnień RBAC: role OWNER, ADMIN, EDITOR oraz VIEWER.
members-invite-btn = Zaproś członka
members-col-collaborator = Współpracownik
members-col-role = Przypisana rola
members-col-since = Członek od

# Tokens View
tokens-title = Osobiste Tokeny Dostępu (PAT)
tokens-desc = Zarządzaj tokenami API (bal_pat_...) do programistycznego dostępu przez REST API (OpenAPI 3.1).
tokens-docs-btn = 📖 Dokumentacja API (/api/docs)
tokens-generate-title = Wygeneruj nowy token
tokens-label-desc = Opis / Nazwa tokena
tokens-placeholder-desc = np. Synchronizacja CLI, Skrypt CI/CD, Klient mobilny
tokens-label-expiration = Ważność
tokens-exp-30d = 30 dni
tokens-exp-90d = 90 dni
tokens-exp-1y = 1 rok
tokens-exp-never = Bezterminowo (brak wygasania)
tokens-generate-btn = Wygeneruj token
tokens-active-title = Aktywne i historyczne tokeny
tokens-col-name = Nazwa tokena
tokens-col-status = Status
tokens-col-last-used = Ostatnie użycie
tokens-col-expires = Wygasa
tokens-col-actions = Akcje
tokens-status-revoked = Unieważniony
tokens-status-expired = Wygasły
tokens-status-active = Aktywny
tokens-never = Nigdy
tokens-revoke-btn = Unieważnij
tokens-revoke-confirm = Czy na pewno chcesz unieważnić ten token? Każdy skrypt lub usługa korzystająca z niego przestanie działać natychmiast.
tokens-empty = Nie wygenerowano jeszcze żadnych tokenów API.
tokens-usage-title = Jak korzystać z Osobistego Tokena Dostępu
tokens-usage-desc = Uwierzytelniaj żądania przekazując token w nagłówku Authorization. Żądania z tokenem PAT nie wymagają tokena CSRF:
tokens-modal-title = Wygenerowano Osobisty Token Dostępu!
tokens-modal-desc = Oto Twój nowy token dla { $name }. Skopiuj go teraz — ze względów bezpieczeństwa nie zostanie wyświetlony ponownie.
tokens-copy-btn = Kopiuj
tokens-copied-btn = Skopiowano!
tokens-modal-warning = Każda osoba posiadająca ten token może uzyskać dostęp do Twojego konta przez REST API bez podawania hasła ani tokena CSRF.
tokens-modal-close-btn = Bezpiecznie zapisałem ten token

# Transactions View
tx-ledger-title = Rejestr Transakcji
tx-ledger-desc = Zunifikowany warsztat przepływów pieniężnych i uzgadniania bankowego.
tx-sync-bank-btn = Synchronizuj z bankiem
tx-new-tx-btn = Nowa transakcja
tx-all-accounts = Wszystkie konta ({ $count })
tx-pending-reconciliation = Oczekujące na uzgodnienie ({ $count })
tx-cleared-ledger = Rozliczone w księdze ({ $count })

# Accounts View
accounts-title = Konta Finansowe
accounts-desc = Płynne aktywa, zobowiązania, wielowalutowe rezerwy gotówkowe i integracje bankowe.
accounts-add-btn = Dodaj konto
accounts-connect-bank-btn = Połącz z bankiem
accounts-total-balance = Łączne saldo na wszystkich kontach
accounts-empty-state = Brak utworzonych kont. Dodaj konto ręczne lub połącz bank, aby rozpocząć.

# Reports View
reports-title = Raporty i Analityka
reports-desc = 10 interaktywnych wglądów finansowych, modeli prognostycznych i symulacji Monte Carlo.
reports-recalculate-all = Przelicz wszystkie
reports-aom-title = 1. Wiek Pieniędzy
reports-aom-desc = Średni ważony wiek wydawanych środków z płynnych przepływów.
reports-income-expense-title = 2. Przychody vs Wydatki
reports-spending-trends-title = 3. Trendy Wydatków
reports-spending-breakdown-title = 4. Podział Wydatków
reports-net-worth-title = 5. Wartość Netto
reports-sankey-title = 6. Przepływy Sankey
reports-forecast-title = 7. Prognoza Salda
reports-party-cashflow-title = 8. Przepływy Kontrahentów
reports-budget-analysis-title = 9. Analiza Budżetu
reports-monte-carlo-title = 10. Żywotność Monte Carlo

# External Parties View
parties-title = Kontrahenci i Podmioty Zewnętrzne
parties-desc = Organizacje, pracodawcy, sklepy i osoby powiązane z transakcjami.
parties-add-btn = Dodaj kontrahenta
parties-all-rules-btn = Wszystkie reguły
parties-smart-suggest-btn = Sugestie reguł AI

# Automations View
automations-title = Automatyzacje
automations-desc = Niestandardowe reguły Lua (Judge + Executor) wykonujące się sekwencyjnie w transakcjach.
automations-new-btn = Nowa automatyzacja

# Notifications & Validation
error-unauthorized = Brak autoryzacji
error-forbidden = Brak wystarczających uprawnień
error-internal = Wystąpił nieoczekiwany błąd. Spróbuj ponownie później.

# Plural test message
test-items-count = { $count ->
    [one] Masz 1 nieprzeczytaną wiadomość
    [few] Masz { $count } nieprzeczytane wiadomości
   *[many] Masz { $count } nieprzeczytanych wiadomości
}
