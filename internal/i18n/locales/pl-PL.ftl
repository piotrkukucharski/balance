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

# Budget Settings View
settings-title = Ustawienia Budżetu
settings-desc = Skonfiguruj preferencje obszaru roboczego, domyślną strefę czasową i parametry księgi.
settings-general-card = Informacje ogólne
settings-budget-name = Nazwa budżetu
settings-default-currency = Domyślna waluta
settings-currency-immutable = Główna waluta jest niezmienna po utworzeniu budżetu
settings-timezone = Strefa czasowa IANA
settings-save-btn = Zapisz ustawienia
settings-danger-zone = Strefa Niebezpieczna
settings-delete-budget-btn = Usuń Budżet

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
tokens-generate-title = Wygeneruj nowy token
tokens-label-desc = Opis / Nazwa tokena
tokens-label-expiration = Ważność
tokens-generate-btn = Wygeneruj token
tokens-active-title = Aktywne i historyczne tokeny

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
