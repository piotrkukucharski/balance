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
settings-danger-zone = Danger Zone
settings-delete-budget-btn = Delete Budget

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
tokens-generate-title = Generate New Token
tokens-label-desc = Token Description / Name
tokens-label-expiration = Expiration
tokens-generate-btn = Generate Token
tokens-active-title = Active and Past Tokens

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
