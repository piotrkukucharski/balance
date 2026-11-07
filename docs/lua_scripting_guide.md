# Balance Lua Scripting Engine & Reference Guide

This document provides a comprehensive guide to scripting with the embedded **Lua 5.1 engine** in **Balance**. It covers syntax, execution contexts, runtime limits, security sandboxing rules, built-in standard and domain APIs, and practical examples for both **Dynamic Target Formulas** (`amount_mode = 'LUA_FORMULA'`) and **Transaction Automations** (Judge & Executor scripts).

---

## 1. Architectural Overview & Security Hardening (6-Pillar Sandbox)

Balance embeds an isolated Lua 5.1 runtime powered by [`gopher-lua`](https://github.com/yuin/gopher-lua). User-supplied scripts are evaluated inside a strict, hardened security sandbox designed to prevent arbitrary code execution, denial of service, memory exhaustion, and ledger math corruption.

### The 6 Non-Negotiable Security Pillars

1. **Pillar 1 — AST Pre-Compilation & Whitelisted Grammar**:
   - Rejects precompiled bytecode (`\x1bLua`). Scripts must be plain text.
   - Size limit: maximum **2 KB** (2,048 bytes) and **50 lines**.
   - Forbidden grammar: `while`, `repeat`, `for` loops, user-defined `function ... end`, `goto`, and string concatenation `..`.

2. **Pillar 2 — Hard Memory, Stack & Instruction Quotas**:
   - Instruction limit: strictly capped at **500 VM instructions** (`L.SetMaxVMInstructions(500)`). Infinite or recursive executions abort immediately with an error.
   - Call stack depth: capped at **16 levels**; registry capped at **256 entries**.
   - Host API call quota: maximum **20 host API invocations** per script evaluation.
   - Hard execution timeout: **50 milliseconds** emergency deadline (`context.WithTimeout`).

3. **Pillar 3 — Strict Minor Units Math (No Floating-Point Leakage)**:
   - All monetary amounts returned and processed by formulas are 64-bit integer **minor units** (e.g. `100 PLN = 10000 minor units`, `19.99 EUR = 1999 minor units`).
   - Floats returned by scripts are checked for `NaN` and `Inf` and safely rounded using `math.Round(val)` before persisting into the financial ledger.

4. **Pillar 4 — Zero Side Effects & Replay Determinism**:
   - No filesystem access, no network sockets, no standard OS libraries (`os`, `io`, `debug`, `loadfile`, `dofile`, `loadstring` are omitted).
   - `math.random` and non-deterministic system clock calls are excluded.
   - For a given snapshot and horizon window, a formula is a pure mathematical function:
     $$\text{Formula}(Snapshot, Window) \rightarrow \text{int64 minor units}$$

5. **Pillar 5 — Zero Database I/O Inside VM (`InMemoryBudgetSnapshot`)**:
   - The Lua VM never holds direct database references (`*sql.DB` or `*sql.Tx`).
   - Prior to evaluation, Go constructs an immutable `InMemoryBudgetSnapshot` containing relevant targets, bucket balances, currency exchange rates, and historical income/spending prefix sums. All queries execute in memory in $O(1)$ time.

6. **Pillar 6 — Ephemeral Isolated `LState` & Graceful Degradation**:
   - Each script evaluation runs in a freshly allocated `LState` with a locked global environment table (`__newindex` raises an error on global writes; `__metatable = "locked"`).
   - If a formula fails (e.g. referencing an unknown target), it **never produces an HTTP 500 or breaks the dashboard**. The evaluator marks the target with a localized warning badge, logs the error, and falls back gracefully.

---

## 2. Scripting Contexts

Balance utilizes Lua scripting in two core domain subsystems:

| Context | Script Type | Expected Return | Primary Purpose |
|---|---|---|---|
| **Target Formula** | Single formula script | `return <int64_minor>` | Calculates dynamic requirement for a target (e.g. loan installments, income percentages, surplus deductions). |
| **Automation Rule** | **Judge** script | `return <boolean>` | Evaluates whether a transaction matches specific criteria (e.g. merchant, note patterns, amount). |
| **Automation Rule** | **Executor** script | (Mutations on `tx` / `account`) | Modifies the matched transaction (assigns bucket, target, party, splits amount). |

---

## 3. Target Formulas (`amount_mode = 'LUA_FORMULA'`)

Dynamic target formulas evaluate the target's required contribution based on live budget state, related targets, or rolling horizons.

### Built-in Global Functions & Objects

#### `money(val)`
Converts major units (decimal number or string) into integer minor units:
```lua
money(2000)     --> 200000 (minor units, e.g. 2,000.00)
money("19.99")  --> 1999   (minor units, e.g. 19.99)
money(12.5)     --> 1250   (minor units, e.g. 12.50)
```

#### `target(ref)`
Looks up another target in the budget by **Name** (case-insensitive) or by **Target ID** (UUIDv7 string).
Returns a read-only table with the following properties:
* `.id` (`string`): Target UUIDv7 identifier.
* `.name` (`string`): Target name (or empty string if unnamed).
* `.planned` (`int64`): Initial planned amount in minor units.
* `.required` (`int64`): Current required amount in minor units.
* `.funded` (`int64`): Currently allocated funded amount in minor units.
* `.spent` (`int64`): Outflow spent against this target in minor units.
* `.priority` (`int`): Priority level (`3` = Mandatory, `2` = Recommended, `1` = Optional).
* `.anchor_due_date` (`int64`): Unix timestamp in epoch seconds of the target's due date (raises an error if target has no due date configured).

#### `targetById(id)`
Explicitly looks up a target by its exact **Target ID**. This is recommended when referencing **unnamed targets** or when avoiding ambiguity between targets that share similar names:
```lua
local loan = targetById("019504a3-7bf1-7000-8000-000000000001").required
```

#### `self`
Inspects the properties of the target currently being evaluated:
* `self.name` (`string`): Target name.
* `self.planned` (`int64`): Planned minor units.
* `self.required` (`int64`): Current required minor units.
* `self.funded` (`int64`): Funded minor units.
* `self.spent` (`int64`): Spent minor units.
* `self.priority` (`int`): Priority.
* `self.anchor_due_date` (`int64`): Target's due date in epoch seconds.

#### `bucket.balance([currency])`
Returns the available envelope balance of the parent bucket in minor units. If `currency` is omitted, defaults to the target's currency:
```lua
local plnBal = bucket.balance()
local eurBal = bucket.balance("EUR")
```

#### `budget.income(days, [currency])`
Returns total external inflows (income) received by user-owned accounts over the past $N$ calendar days ($1 \le \text{days} \le 3650$), excluding internal transfers:
```lua
local income30d = budget.income(30, "PLN")
```

#### `budget.spent(days, [currency])`
Returns total external outflows spent across user-owned accounts over the past $N$ calendar days ($1 \le \text{days} \le 3650$), excluding internal transfers.

#### `window.days`
An integer constant representing the currently evaluated rolling horizon window length in days (default `30`, or `90`, `365`).

#### `runtime.today`
Unix timestamp in seconds representing the start of the current day (`00:00:00 UTC`) in the budget's configured timezone.

#### `financial.pmt(rate, nper, pv, [fv], [type])`
Calculates constant payment for a loan based on constant interest rate and period count (standard Excel PMT formula).
* `rate` (`number`): Interest rate per period (e.g. `0.06 / 12` for 6% annual paid monthly).
* `nper` (`number`): Total number of payment periods (e.g. months remaining).
* `pv` (`number`): Present value / principal (loan amount).
* Convention: returns negative cash flow, so use `-financial.pmt(...)`:
```lua
local nper = date.diff_months(self.anchor_due_date, runtime.today)
local r = 0.06 / 12
local pv = 300000
local pmt = -financial.pmt(r, nper, pv)
return money(pmt)
```

#### `date` Library
* `date.diff_months(t1, t2)`: Whole months between epoch timestamps $t_1$ and $t_2$ ($t_1 - t_2$).
* `date.year(t)`, `date.month(t)`, `date.day(t)`, `date.weekday(t)`: Extracts calendar parts.
* `date.format(t, layout?)`: Formats timestamp (default `"2006-01-02"`).
* `date.parse(str, layout?)`: Parses date string into Unix microsecond timestamp.

#### `fx` Library
* `fx.rate(base, target)`: Returns currency exchange rate as a float number.
* `fx.convert(amount_minor, from, to)`: Converts minor units between currencies using integer triangulation.

#### Allowed Math Functions
Whitelisted standard mathematical operations:
* `math.max(...)`, `math.min(...)`
* `math.floor(val)`, `math.ceil(val)`, `math.abs(val)`
* `math.fmod(a, b)`, `math.pow(base, exp)`, `math.sqrt(val)`
* `tonumber(val)`

---

## 4. Transaction Automations (Judge & Executor)

Automations execute during transaction ingest or batch pipelines to categorize, tag, split, or assign transactions.

### Judge Scripts
A Judge script inspects a transaction (`tx`) and returns a boolean (`true` or `false`):
```lua
-- Match ride-hailing services in transaction notes
return text.contains(text.lower(tx.notes), "uber") or text.contains(text.lower(tx.notes), "bolt")
```

#### Read-Only `tx` Fields in Judge
* `tx.id`: Transaction identifier.
* `tx.notes`: Transaction notes / description.
* `tx.amount`: Transaction amount in minor units.
* `tx.source_account_id`, `tx.dest_account_id`: Account IDs.
* `tx.source_account_name`, `tx.dest_account_name`: Account names.
* `tx.party_id`, `tx.party_name`: Counterparty.
* `tx.bucket_id`, `tx.bucket_name`: Currently assigned envelope.
* `tx.target_id`, `tx.target_name`: Currently assigned target.
* `tx.is_outflow`, `tx.is_inflow`, `tx.is_transfer`: Direction flags.
* `tx.date`: Transaction timestamp in microseconds.

#### `text` Helper Library
* `text.contains(str, substr)`: Case-sensitive substring check.
* `text.starts_with(str, prefix)`, `text.ends_with(str, suffix)`.
* `text.lower(str)`, `text.upper(str)`, `text.trim(str)`.
* `text.regex_match(pattern, str)`: Regular expression matching.

### Executor Scripts
Executor scripts modify the matched transaction using designated mutator methods:
```lua
-- Assign bucket and target by ID or Name
tx.set_bucket("Transport")
tx.set_target("Metro Pass")

-- Or by Target ID directly (ideal for unnamed targets)
tx.set_target_by_id("019504a3-7bf1-7000-8000-000000000001")

-- Update notes or party
tx.set_notes("Public Transit Pass")
tx.set_party_by_name("ZTM Warszawa")

-- Split transaction across envelopes
tx.split({
    { amount = money(50), bucket = "Transport" },
    { amount = money(30), bucket = "Groceries" }
})
```

---

## 5. Practical Formula Examples

### Example 1: Complementary / Mortgage Overpayment
Keep total monthly expenditure fixed at 2,000 PLN, calculating the remainder after mandatory installment:
```lua
return math.max(0, money(2000) - target("Required Mortgage Installment").required)
```

### Example 2: Referencing an Unnamed Target by ID
Calculate supplementary buffer depending on an unnamed base target:
```lua
local base = targetById("019504a3-7bf1-7000-8000-000000000001").required
return math.floor(base * 0.15)
```

### Example 3: Periodic Recurrent Expense (Quarterly / Annual)
Prorate quarterly expense (125 PLN every 90 days) for the current evaluated window:
```lua
return math.ceil(money(125) * window.days / 90)
```

### Example 4: Percentage of 30-Day Income Capped
Save 15% of total income over the past month, capped at 5,000 PLN:
```lua
return math.min(money(5000), math.floor(budget.income(30, "PLN") * 0.15))
```

### Example 5: Multi-Currency Hedged Goal
Target denominated in PLN that dynamically requires the equivalent of 500 EUR:
```lua
return fx.convert(money(500), "EUR", "PLN")
```

### Example 6: Loan Installment to Anchor Due Date via `financial.pmt`
Automatically compute the monthly annuity payment needed to pay off a 300,000 PLN loan at 6% annual rate by the target's anchor due date:
```lua
local nper = date.diff_months(self.anchor_due_date, runtime.today)
local r = 0.06 / 12
local pv = 300000
local pmt = -financial.pmt(r, nper, pv)
return money(pmt)
```

---

## 6. Limits & Error Handling Quick Reference

| Restriction | Limit | Behavior on Violation |
|---|---|---|
| **Max script size** | 2,048 bytes / 50 lines | Compilation rejection (`ValidateLuaScript`) |
| **Max VM instructions** | 500 instructions | Aborts execution (`ErrInstructionLimitExceeded`) |
| **Max host API calls** | 20 calls | Aborts execution (`ErrHostCallLimitExceeded`) |
| **Max call stack depth**| 16 frames | Memory allocation failure |
| **Max execution time** | 50 milliseconds | Context deadline timeout |
| **Target lookup failure** | Non-existent ref | Raises runtime Lua error (handled gracefully by projections) |
| **Anchor due date unset**| Accessing missing due date | Raises runtime Lua error: `anchor_due_date is not set for target` |
