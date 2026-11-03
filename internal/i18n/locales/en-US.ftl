# Balance Fluent Translations (en-US)
# Default locale metadata
locale-name = English (US)
locale-number-grouping = ,
locale-number-decimal = .
locale-date-format = YYYY-MM-DD
locale-time-format = 24h
locale-currency-pattern = { $amount } { $currency }

# Global / Navigation
app-name = Balance
nav-budget = 📂 1. Budget (Buckets)
nav-accounts = 💳 2. Accounts
nav-transactions = 💸 3. Transactions
nav-automations = ⚡ 4. Automations
nav-reports = 📊 5. Reports
nav-parties = 🏢 6. External Parties
nav-settings = ⚙️ 7. Settings
nav-settings-general = General
nav-settings-members = 👥 Members
nav-settings-tokens = 🔑 API Tokens
nav-settings-profile = 👤 Profile & Preferences
nav-logout = Log Out
nav-back = ← Back

# Bottom Dock (Mobile)
dock-budget = Budget
dock-accounts = Accounts
dock-transactions = Txns
dock-automations = Rules
dock-reports = Reports
dock-settings = Settings

# Auth (Login / Register)
auth-login-title = Welcome to Balance
auth-login-subtitle = Log in to manage your budget and envelopes.
auth-credentials = CREDENTIALS
auth-email = Email Address
auth-password = Password
auth-signin-btn = Sign In
auth-no-account = Don't have an account?
auth-create-one = Create one here
auth-registration-disabled = Public registration is currently disabled by administrator.
auth-register-title = Create your account
auth-register-subtitle = Envelope budgeting with multi-currency clarity.
auth-enter-details = ENTER DETAILS
auth-fullname = Full Name / Display Name
auth-get-started-btn = Get Started Free
auth-already-account = Already have an account?
auth-login-here = Log in here
auth-invalid-credentials = Invalid email or password. Please try again.
auth-registration-disabled-err = User registration is currently disabled.

# Profile & Preferences View
profile-title = Profile & Preferences
profile-desc = Manage personal account details, language, and regional date/time/number formatting preferences.
profile-saved-success = ✓ Preferences updated successfully.
profile-section-personal = Personal Details
profile-section-localization = Localization & Formats
profile-label-display-name = Display Name
profile-label-email = Email Address
profile-label-language = Interface Language
profile-label-date-format = Preferred Date Format
profile-label-time-format = Preferred Time Format
profile-label-number-format = Preferred Number Format
profile-option-auto-default = Default for Language (Auto)
profile-live-preview = Live Preview
profile-preview-date = Date Preview:
profile-preview-timestamp = Timestamp Preview:
profile-preview-number = Number Preview:
profile-preview-currency = Currency Preview:
profile-btn-save = Save Preferences

# Common Actions & Badges
action-save = Save
action-cancel = Cancel
action-delete = Delete
action-edit = Edit
action-confirm = Confirm
action-close = Close
action-add = Add
action-rename = Rename
action-clone = Clone
action-restore = Restore
action-search = Search
action-filter = Filter
action-refresh = Refresh
action-sync = Sync
badge-active = ACTIVE
badge-deleted = DELETED

# Budgets List View
budgets-list-title = Your Budgets
budgets-list-desc = Select an active budget or create a new envelope workspace.
budgets-list-new-btn = New Budget
budgets-list-clone-btn = Clone Budget
budgets-list-delete-btn = Delete Budget
budgets-list-timezone = Timezone: { $tz }
budgets-list-no-budgets = You have not created any budgets yet.
budgets-list-default-currency = Default Currency
budgets-list-open-btn = Open
budgets-list-link-bank-btn = Link Bank

# Budget (Buckets) View
budget-title = Budget
budget-age-of-money = Age of Money: { $days } days
budget-rta-title = Ready to Assign
budget-rta-assigned-desc = All money assigned to envelopes
budget-needed-title = Needed ({ $horizon })
budget-mandatory-needed = of which Mandatory:
budget-all-mandatory-funded = All mandatory targets funded
budget-auto-assign-btn = Auto-Assign by Priority
budget-filter-all = All
budget-filter-attention = Needs attention
budget-filter-overspent = Overspent
budget-filter-buffer = Has buffer
budget-expand-all = Expand all
budget-collapse-all = Collapse all
budget-search-placeholder = Search envelopes...
budget-new-bucket-btn = New Bucket
budget-accounts-summary = Accounts Summary
budget-no-accounts = No accounts connected yet.

# Budget Settings View
settings-title = Budget Settings
settings-desc = Configure workspace preferences, default timezones, and ledger parameters.
settings-general-card = General Information
settings-budget-name = Budget Name
settings-default-currency = Default Currency
settings-currency-immutable = Base currency is immutable once provisioned
settings-timezone = IANA Timezone
settings-save-btn = Save Settings
settings-role-notice = You are viewing budget settings with { $role } permissions. Only the OWNER can update budget properties.

# Time Horizons Card
settings-horizons-card = Time Horizons
settings-horizons-desc = Configure rolling and calendar requirement windows. Each member can pick their default view.
settings-horizons-user-default = Your Default Horizon
settings-horizons-user-default-desc = The horizon activated automatically when you open this budget.
settings-horizons-save-pref-btn = Save Default Horizon
settings-horizons-table-title = Available Horizons
settings-horizons-col-horizon = Horizon
settings-horizons-col-type = Type
settings-horizons-col-status = Status
settings-horizons-col-actions = Actions
settings-horizons-type-monthly = Calendar Month
settings-horizons-type-annual = Calendar Year
settings-horizons-type-rolling = Rolling Window
settings-horizons-type-calendar-day = Calendar Day of Month
settings-horizons-status-default = Active Default
settings-horizons-status-visible = Visible
settings-horizons-status-hidden = Hidden
settings-horizons-btn-hide = Hide
settings-horizons-btn-show = Show
settings-horizons-btn-delete = Delete
settings-horizons-add-title = Add New Horizon
settings-horizons-add-type = Horizon Type
settings-horizons-opt-rolling = Rolling Days (1-3650)
settings-horizons-opt-day-of-month = Calendar Day of Month (1-28)
settings-horizons-label-days = Number of Days
settings-horizons-label-day-num = Day of Month
settings-horizons-btn-add = Add Horizon
settings-horizons-modal-title = Reassign Users Before Hiding/Deleting
settings-horizons-modal-desc = The horizon you are about to hide or delete is currently set as the default for the following budget members:
settings-horizons-modal-select-fallback = Select an alternative visible horizon for these members:
settings-horizons-modal-confirm-btn = Confirm and Reassign
settings-horizons-owner-only-notice = Only the budget OWNER can add, hide, or delete budget horizons.

# Horizon Labels
horizon-name-monthly = Monthly
horizon-name-annual = Annual
horizon-name-rolling = { $days }d
horizon-name-calendar-toolbar = Until { $day }th
horizon-name-calendar-full = Until the { $day }th of the month

settings-personal-pref-title = Personal Preferences
settings-personal-pref-desc = Configure your personal date format ({ $dateFormat }) and time format ({ $timeFormat }).
settings-personal-pref-btn = Customize Formats →
settings-debug-card-title = Diagnostic Tools (Debug)
settings-debug-card-desc = Check financial integrity, orphaned balances, and run balance repair actions.
settings-debug-card-btn = Open Debug Tools →
settings-danger-zone = Danger Zone
settings-danger-zone-desc = Permanently delete this budget envelope, ledger records, transactions, and account history. This action cannot be undone.
settings-delete-budget-btn = Delete Budget
settings-delete-confirm = Are you absolutely sure you want to delete this budget? All data will be permanently wiped.
settings-danger-owner-only = Only the budget OWNER can delete or destroy this workspace.

# Diagnostic Tools (Debug) View
debug-title = Diagnostic Tools (Debug)
debug-desc = Audit ledger integrity in real-time, detect orphaned projections, and trigger transactional repair routines.
debug-back-to-settings = ← Back to Settings
debug-badge = Diagnostic Tools (Debug)
debug-toast-success = Operation completed
debug-integrations-title = Integrations (AI & GoCardless)
debug-integrations-desc = Verify authorization, connectivity, and status of external service providers.
debug-gemini-title = Google Gemini API
debug-gemini-status-untested = Not checked
debug-gemini-status-checking = Checking...
debug-gemini-status-ok = Operational
debug-gemini-desc = Verify communication with LLM model on prompt test "2 + 2 =".
debug-gemini-btn = Run AI Audit
debug-gocardless-title = GoCardless (Open Banking)
debug-gocardless-status-untested = Not checked
debug-gocardless-status-checking = Checking...
debug-gocardless-status-ok = Operational
debug-gocardless-desc = Verify OAuth token authorization and retrieving banking institutions from the API.
debug-gocardless-btn = Run GoCardless Audit
debug-integrations-status-error = Error ({ $status })
debug-integrations-status-failure = Failure
debug-integrations-http-status = Status HTTP: { $status }
debug-integrations-error = Error: { $error }
debug-integrations-api-response = API Response:
debug-integrations-request-failed = Request execution error: { $error }
debug-ai-success = Connection to Google Gemini API ({ $model }) works properly. Response to '2 + 2 =': { $reply }
debug-gocardless-success = Connection to GoCardless API successful. Retrieved { $count } banking institutions for country { $country }.
debug-gocardless-no-keys = Missing GOCARDLESS_SECRET_ID / GOCARDLESS_SECRET_KEY credentials in the environment
debug-gocardless-api-error = GoCardless API communication error: { $error }

# Debug Audit Card
debug-audit-card-title = Ledger Consistency & Balance Integrity
debug-audit-card-desc = Real-time consistency checks evaluated in an isolated transaction.
debug-audit-all-passed = All Ledger Checks Passed
debug-audit-anomalies-detected = Anomalies Detected
debug-audit-check1-title = 1. Orphaned Balances & Transactions (Accounts deleted from accounts_current)
debug-audit-check1-clean = No orphaned balances or transactions associated with non-existent accounts.
debug-audit-orphaned-balances-alert = { $count } orphaned balance records were detected in account_currency_balances!
debug-audit-orphaned-accounts-alert = { $count } non-existent accounts associated with transactions or balances were detected:
debug-audit-orphaned-txs-details = Details of Orphaned Transactions ({ $count }):
debug-audit-col-account-id = Account ID
debug-audit-col-currency = Currency
debug-audit-col-amount = Amount
debug-audit-col-account-name = Account Name (History)
debug-audit-col-budget-txs = Budget Transactions
debug-audit-col-raw-txs = Raw Transactions
debug-audit-col-history-status = Status in History
debug-audit-col-actions = Corrective Actions
debug-audit-col-type = Type
debug-audit-col-roles = Roles of the Missing Account
debug-audit-col-missing-acct = Missing Account ID
debug-audit-col-notes-payee = Notes / Payee
debug-audit-btn-resurrect = Resurrect Account
debug-audit-not-in-history = Not in history
debug-audit-confirm-resurrect = Restore account "{ $name }" in closed state (is_closed=1)?
debug-audit-check2-title = 2. Transaction Consistency with Account Balances
debug-audit-check2-clean = All account balances exactly match the net sum of budget transactions.
debug-audit-check2-discrepancies-alert = Found { $count } accounts with discrepancy between stored balance and transaction history!
debug-audit-col-stored = Stored
debug-audit-col-computed = Computed
debug-audit-col-diff = Diff
debug-audit-check3-title = 3. Fundamental Equation (Cash == Envelopes + RTA)
debug-audit-status-balanced = Balanced
debug-audit-status-unbalanced = Unbalanced
debug-audit-btn-rerun = Run Audit Again
debug-audit-btn-cleanup-orphaned = Remove Orphaned Balances
debug-audit-confirm-cleanup-orphaned = Are you sure you want to delete orphaned balance records?
debug-audit-btn-delete-orphaned-txs = Delete Orphaned Transactions
debug-audit-confirm-delete-orphaned-txs = Are you sure you want to permanently delete all orphaned budget and raw transactions?
debug-audit-btn-reconcile = Generate Balance Adjustment
debug-audit-confirm-reconcile = Create automatic adjustment transactions for detected balance discrepancies?
debug-notify-audit-clean = Integrity audit completed successfully.
debug-notify-audit-anomalies = Audit completed: balance anomalies detected.
debug-notify-cleanup-balances-success = Cleaned up { $count } orphaned balance records.
debug-notify-resurrect-success = Account "{ $name }" was successfully resurrected as closed (is_closed=1).
debug-notify-resurrect-not-found = Account does not exist in accounts history
debug-notify-delete-txs-success = Successfully deleted { $bCount } budget transactions and { $rCount } raw transactions.
debug-notify-reconcile-success = Generated { $count } reconciliation adjustment transactions.
debug-notify-reconcile-none = No balance discrepancies to reconcile.

# Members View
members-title = Members & Access Control
members-desc = Multi-user RBAC hierarchy: OWNER, ADMIN, EDITOR, and VIEWER roles.
members-invite-btn = Invite Member
members-col-collaborator = Collaborator
members-col-role = Assigned Role
members-col-since = Member Since

# Tokens View
tokens-title = Personal Access Tokens (PAT)
tokens-desc = Manage API tokens (bal_pat_...) for programmatic REST API access (OpenAPI 3.1).
tokens-docs-btn = 📖 Interactive API Docs (/api/docs)
tokens-generate-title = Generate New Token
tokens-label-desc = Token Description / Name
tokens-placeholder-desc = e.g. CLI Sync, CI/CD Script, Mobile Client
tokens-label-expiration = Expiration
tokens-exp-30d = 30 days
tokens-exp-90d = 90 days
tokens-exp-1y = 1 year
tokens-exp-never = Never (No expiration)
tokens-generate-btn = Generate Token
tokens-active-title = Active and Past Tokens
tokens-col-name = Token Name
tokens-col-status = Status
tokens-col-last-used = Last Used
tokens-col-expires = Expires
tokens-col-actions = Actions
tokens-status-revoked = Revoked
tokens-status-expired = Expired
tokens-status-active = Active
tokens-never = Never
tokens-revoke-btn = Revoke
tokens-revoke-confirm = Are you sure you want to revoke this token? Any script or service using it will stop working immediately.
tokens-empty = No API tokens generated yet.
tokens-usage-title = How to use your Personal Access Token
tokens-usage-desc = Authenticate requests by passing the token in the Authorization header. PAT requests are exempt from CSRF checks:
tokens-modal-title = Personal Access Token Generated!
tokens-modal-desc = Here is your new token for { $name }. Make sure to copy it now — for security reasons, it will not be shown again.
tokens-copy-btn = Copy
tokens-copied-btn = Copied!
tokens-modal-warning = Anyone with this token can access your account through the REST API without entering your password or CSRF token.
tokens-modal-close-btn = I have safely stored this token

# Transactions View
tx-ledger-title = Transactions Ledger
tx-ledger-desc = Unified double-entry cashflow workbench and rapid bank reconciliation.
tx-sync-bank-btn = Sync with Bank
tx-new-tx-btn = New Transaction
tx-all-accounts = All Accounts ({ $count })
tx-pending-reconciliation = Pending Bank Reconciliation ({ $count })
tx-cleared-ledger = Cleared Ledger Transactions ({ $count })

# Accounts View
accounts-title = Financial Accounts
accounts-desc = Liquid assets, liabilities, multi-currency cash reserves, and automated bank feed connections.
accounts-add-btn = Add Account
accounts-connect-bank-btn = Connect Bank Feed
accounts-total-balance = Total Balance Across Accounts
accounts-empty-state = No accounts created yet. Add manual account or link bank feed to get started.

# Reports View
reports-title = Reports & Analytics
reports-desc = 10 interactive financial insights, forecasting models, and Monte Carlo longevity simulations.
reports-recalculate-all = Recalculate All
reports-aom-title = 1. Age of Money
reports-aom-desc = Weighted average age of money spent across liquid outflows.
reports-income-expense-title = 2. Income vs Expense
reports-spending-trends-title = 3. Spending Trends
reports-spending-breakdown-title = 4. Spending Breakdown
reports-net-worth-title = 5. Net Worth
reports-sankey-title = 6. Cashflow Sankey
reports-forecast-title = 7. Balance Forecast
reports-party-cashflow-title = 8. Party Cashflow
reports-budget-analysis-title = 9. Budget Analysis
reports-monte-carlo-title = 10. Monte Carlo Longevity

# External Parties View
parties-title = External Parties & Counterparties
parties-desc = Organizations, employers, merchants, and individuals linked to transactions.
parties-add-btn = Add Counterparty
parties-all-rules-btn = All Rules
parties-smart-suggest-btn = AI Smart Rules Suggestion

# Automations View
automations-title = Automations
automations-desc = Custom Lua rules (Judge + Executor) executing sequentially to automate transaction categories, splits, and updates.
automations-new-btn = New Automation

# Notifications & Validation
error-unauthorized = Unauthorized
error-forbidden = Insufficient permissions
error-internal = An unexpected error occurred. Please try again.

# Plural test message
test-items-count = { $count ->
    [one] You have 1 unread message
   *[other] You have { $count } unread messages
}
