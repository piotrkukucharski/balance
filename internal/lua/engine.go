package lua

import (
	"context"
	"errors"
	"fmt"
	"math"
	"regexp"
	"strings"
	"time"

	"github.com/balance/balance/internal/domain"
	glua "github.com/yuin/gopher-lua"
	"github.com/yuin/gopher-lua/parse"
)

const (
	MaxScriptBytes     = 2048
	MaxScriptLines     = 50
	MaxHostAPICalls    = 32
	MaxSafeTargetMinor = domain.MaxSafeAmountMinor
	EmergencyTimeout   = 50 * time.Millisecond
)

var (
	ErrScriptTooLarge          = errors.New("script exceeds 2048 bytes limit")
	ErrScriptTooManyLines      = errors.New("script exceeds 50 lines limit")
	ErrBytecodeHeaderForbidden = errors.New("precompiled bytecode header is strictly forbidden")
	ErrForbiddenToken          = errors.New("forbidden language construct (loops, function declarations, string concatenation are blocked)")
	ErrHostCallLimitExceeded   = errors.New("lua host API call quota exceeded (32 calls)")
	ErrInvalidReturnValue      = errors.New("lua formula must return a single numeric integer in minor units")
)

// TargetMeta represents an in-memory target snapshot
type TargetMeta struct {
	ID       string
	Name     string
	BucketID string
	Planned  int64
	Required int64
	Funded   int64
	Spent    int64
	Priority int
	Currency string
}

// BucketMeta represents an in-memory bucket snapshot
type BucketMeta struct {
	ID       string
	Name     string
	Currency string
}

// AccountMeta represents an in-memory account snapshot
type AccountMeta struct {
	ID          string
	Name        string
	PartyID     string
	Currency    string
	IsUserOwned bool
}

// PartyMeta represents an in-memory counterparty snapshot
type PartyMeta struct {
	ID          string
	Name        string
	IsUserOwned bool
}

// AutomationTxContext represents a budget transaction passed into Lua automation scripts.
type AutomationTxContext struct {
	ID                   string
	SourceAccountID      string
	DestinationAccountID string
	SourceAmountMinor    int64
	SourceCurrency       string
	DestAmountMinor      int64
	DestCurrency         string
	BucketID             *string
	TargetID             *string
	TransactionDate      int64
	Notes                *string

	SourceAccountName string
	DestAccountName   string
	BucketName        string
	TargetName        string
	PartyName         string
	PartyID           string
	IsUserOwnedSource bool
	IsUserOwnedDest   bool
}

// SplitItem defines one component resulting from tx.split({...}).
type SplitItem struct {
	AmountMinor int64
	BucketID    *string
	TargetID    *string
	Notes       *string
}

// AutomationOutcome records the side-effects of an executor script.
type AutomationOutcome struct {
	Deleted   bool
	Modified  bool
	Splits    []SplitItem
	UpdatedTx AutomationTxContext
}

// InMemoryBudgetSnapshot provides O(1) in-memory financial data to the VM without database I/O.
type InMemoryBudgetSnapshot struct {
	Targets        map[string]*TargetMeta  // lookup by ID and lowercase Name
	Buckets        map[string]*BucketMeta  // lookup by ID and lowercase Name
	Accounts       map[string]*AccountMeta // lookup by ID and lowercase Name
	Parties        map[string]*PartyMeta   // lookup by ID and lowercase Name
	BucketBalances map[string]int64        // currency -> minor balance
	FXRates        map[string]int64        // "EUR_PLN" -> rate_micros
	InflowsPast    map[string][]int64      // currency -> daily inflow prefix sum
	OutflowsPast   map[string][]int64      // currency -> daily outflow prefix sum
	WindowDays     int
	TargetCurrency string
}

// ValidateLuaScript tokens and syntax for security compliance.
func ValidateLuaScript(script string) error {
	if len(script) > MaxScriptBytes {
		return ErrScriptTooLarge
	}
	if strings.Count(script, "\n") > MaxScriptLines {
		return ErrScriptTooManyLines
	}
	// Check for bytecode header \x1bLua
	if strings.Contains(script, "\x1bLua") {
		return ErrBytecodeHeaderForbidden
	}

	// Token level scan to enforce AST grammar bans:
	// No loops: while, repeat, for
	// No function declarations: function
	// No goto: goto
	// No concat: ..
	scanner := parse.NewScanner(strings.NewReader(script), "<formula>")
	lexer := &parse.Lexer{}
	for {
		tok, err := scanner.Scan(lexer)
		if err != nil || tok.Type == parse.EOF {
			break
		}
		switch tok.Type {
		case parse.TWhile, parse.TRepeat, parse.TFor, parse.TGoto:
			return fmt.Errorf("%w: loops and jumps are forbidden", ErrForbiddenToken)
		case parse.TFunction:
			return fmt.Errorf("%w: user-defined functions are forbidden", ErrForbiddenToken)
		case parse.T2Comma: // .. in Lua tokenizer is T2Comma
			return fmt.Errorf("%w: string concatenation (..) is forbidden to prevent memory exhaustion", ErrForbiddenToken)
		}
	}

	// Verify valid Lua syntax
	reader := strings.NewReader(script)
	if _, err := parse.Parse(reader, "<formula>"); err != nil {
		return fmt.Errorf("lua syntax error: %w", err)
	}

	return nil
}

// ValidateJudgeScript validates a judge script, automatically treating single-line expressions as return statements.
func ValidateJudgeScript(script string) error {
	trimmed := strings.TrimSpace(script)
	if !strings.HasPrefix(trimmed, "return ") && !strings.HasPrefix(trimmed, "if ") && !strings.Contains(trimmed, "\n") {
		trimmed = "return " + trimmed
	}
	return ValidateLuaScript(trimmed)
}

// ExtractTargetDependencies parses script and returns target references.
func ExtractTargetDependencies(script string) ([]string, error) {
	if err := ValidateLuaScript(script); err != nil {
		return nil, err
	}
	var deps []string
	matches := targetRefRegex.FindAllStringSubmatch(script, -1)
	for _, m := range matches {
		if len(m) > 1 {
			deps = append(deps, m[1])
		}
	}
	return deps, nil
}

var targetRefRegex = regexp.MustCompile(`target\(\s*["']([^"']+)["']\s*\)`)

// EvaluateFormula executes a Lua formula inside the hardened 6-pillar sandbox.
func EvaluateFormula(ctx context.Context, script string, snap *InMemoryBudgetSnapshot) (int64, error) {
	if err := ValidateLuaScript(script); err != nil {
		return 0, err
	}

	ctx, cancel := context.WithTimeout(ctx, EmergencyTimeout)
	defer cancel()

	// Pillar 2: Hard Memory, Stack, Instruction Quota & Emergency Timeout
	L := glua.NewState(glua.Options{
		CallStackSize:   16,
		RegistrySize:    128,
		RegistryMaxSize: 256,
		SkipOpenLibs:    true,
	})
	defer L.Close()

	L.SetContext(ctx)

	hostCallCount := 0
	checkHostCall := func() error {
		hostCallCount++
		if hostCallCount > MaxHostAPICalls {
			return ErrHostCallLimitExceeded
		}
		return nil
	}

	// Register Pillar 1: Whitelisted math library & domain functions
	registerSandboxedEnvironment(L, snap, checkHostCall)

	// Execute
	if err := L.DoString(script); err != nil {
		return 0, fmt.Errorf("lua execution error: %w", err)
	}

	// Read return value (Pillar 3: NaN/Inf/int64 overflow sanitizer)
	top := L.GetTop()
	if top < 1 {
		return 0, ErrInvalidReturnValue
	}

	retVal := L.Get(top)
	numVal, ok := retVal.(glua.LNumber)
	if !ok {
		return 0, ErrInvalidReturnValue
	}

	f := float64(numVal)
	if math.IsNaN(f) || math.IsInf(f, 0) {
		return 0, errors.New("lua formula returned NaN or Inf")
	}

	if f < 0 {
		return 0, nil // Clamped to non-negative
	}

	if f > float64(MaxSafeTargetMinor) {
		return 0, errors.New("lua formula returned value exceeding MaxSafeTargetMinor")
	}

	return int64(math.Round(f)), nil
}

func registerSandboxedEnvironment(L *glua.LState, snap *InMemoryBudgetSnapshot, checkCall func() error) {
	if snap == nil {
		snap = &InMemoryBudgetSnapshot{}
	}
	if snap.Targets == nil {
		snap.Targets = make(map[string]*TargetMeta)
	}
	if snap.BucketBalances == nil {
		snap.BucketBalances = make(map[string]int64)
	}
	if snap.FXRates == nil {
		snap.FXRates = make(map[string]int64)
	}
	if snap.InflowsPast == nil {
		snap.InflowsPast = make(map[string][]int64)
	}
	if snap.OutflowsPast == nil {
		snap.OutflowsPast = make(map[string][]int64)
	}

	// Whitelisted math table
	mathTable := L.NewTable()
	L.SetField(mathTable, "abs", L.NewFunction(luaMathAbs))
	L.SetField(mathTable, "ceil", L.NewFunction(luaMathCeil))
	L.SetField(mathTable, "floor", L.NewFunction(luaMathFloor))
	L.SetField(mathTable, "fmod", L.NewFunction(luaMathFmod))
	L.SetField(mathTable, "max", L.NewFunction(luaMathMax))
	L.SetField(mathTable, "min", L.NewFunction(luaMathMin))
	L.SetField(mathTable, "pow", L.NewFunction(luaMathPow))
	L.SetField(mathTable, "sqrt", L.NewFunction(luaMathSqrt))
	L.SetGlobal("math", mathTable)

	// tonumber
	L.SetGlobal("tonumber", L.NewFunction(luaToNumber))

	// money(val)
	L.SetGlobal("money", L.NewFunction(func(state *glua.LState) int {
		if err := checkCall(); err != nil {
			state.RaiseError("%s", err.Error())
			return 0
		}
		val := state.Get(1)
		var minor int64
		switch v := val.(type) {
		case glua.LNumber:
			minor = int64(math.Round(float64(v) * 100))
		case glua.LString:
			var f float64
			fmt.Sscanf(string(v), "%f", &f)
			minor = int64(math.Round(f * 100))
		default:
			state.RaiseError("money() expects number or numeric string")
			return 0
		}
		state.Push(glua.LNumber(minor))
		return 1
	}))

	// target(ref)
	L.SetGlobal("target", L.NewFunction(func(state *glua.LState) int {
		if err := checkCall(); err != nil {
			state.RaiseError("%s", err.Error())
			return 0
		}
		ref := state.CheckString(1)
		var meta *TargetMeta
		if snap.Targets != nil {
			meta = snap.Targets[ref]
			if meta == nil {
				meta = snap.Targets[strings.ToLower(ref)]
			}
		}
		if meta == nil {
			state.RaiseError("target %q not found in budget", ref)
			return 0
		}

		tbl := state.NewTable()
		state.SetField(tbl, "planned", glua.LNumber(meta.Planned))
		state.SetField(tbl, "required", glua.LNumber(meta.Required))
		state.SetField(tbl, "funded", glua.LNumber(meta.Funded))
		state.SetField(tbl, "spent", glua.LNumber(meta.Spent))
		state.SetField(tbl, "priority", glua.LNumber(meta.Priority))
		state.Push(tbl)
		return 1
	}))

	// bucket.balance(currency?)
	bucketTable := L.NewTable()
	L.SetField(bucketTable, "balance", L.NewFunction(func(state *glua.LState) int {
		if err := checkCall(); err != nil {
			state.RaiseError("%s", err.Error())
			return 0
		}
		curr := snap.TargetCurrency
		if state.GetTop() >= 1 {
			curr = state.CheckString(1)
		}
		var bal int64
		if snap.BucketBalances != nil {
			bal = snap.BucketBalances[curr]
		}
		state.Push(glua.LNumber(bal))
		return 1
	}))
	L.SetGlobal("bucket", bucketTable)

	// fx.rate and fx.convert
	fxTable := L.NewTable()
	L.SetField(fxTable, "rate", L.NewFunction(func(state *glua.LState) int {
		if err := checkCall(); err != nil {
			state.RaiseError("%s", err.Error())
			return 0
		}
		base := state.CheckString(1)
		target := state.CheckString(2)
		if base == target {
			state.Push(glua.LNumber(1.0))
			return 1
		}
		key := base + "_" + target
		rateMicros, ok := snap.FXRates[key]
		if !ok {
			state.RaiseError("no FX rate available for %s to %s", base, target)
			return 0
		}
		state.Push(glua.LNumber(float64(rateMicros) / 1_000_000.0))
		return 1
	}))
	L.SetField(fxTable, "convert", L.NewFunction(func(state *glua.LState) int {
		if err := checkCall(); err != nil {
			state.RaiseError("%s", err.Error())
			return 0
		}
		amountMinor := int64(state.CheckNumber(1))
		base := state.CheckString(2)
		target := state.CheckString(3)
		if base == target {
			state.Push(glua.LNumber(amountMinor))
			return 1
		}
		key := base + "_" + target
		rateMicros, ok := snap.FXRates[key]
		if !ok {
			state.RaiseError("no FX rate available for %s to %s", base, target)
			return 0
		}
		res, err := domain.ConvertCurrencySafe(amountMinor, rateMicros)
		if err != nil {
			state.RaiseError("%s", err.Error())
			return 0
		}
		state.Push(glua.LNumber(res))
		return 1
	}))
	L.SetGlobal("fx", fxTable)

	// budget.income(days, currency?) and budget.spent(days, currency?)
	budgetTable := L.NewTable()
	L.SetField(budgetTable, "income", L.NewFunction(func(state *glua.LState) int {
		if err := checkCall(); err != nil {
			state.RaiseError("%s", err.Error())
			return 0
		}
		days := state.CheckInt(1)
		if days < 1 || days > 3650 {
			state.RaiseError("days must be between 1 and 3650")
			return 0
		}
		curr := snap.TargetCurrency
		if state.GetTop() >= 2 {
			curr = state.CheckString(2)
		}
		var total int64
		if snap.InflowsPast != nil {
			arr := snap.InflowsPast[curr]
			if len(arr) > 0 {
				idx := days
				if idx > len(arr) {
					idx = len(arr)
				}
				total = arr[idx-1]
			}
		}
		state.Push(glua.LNumber(total))
		return 1
	}))
	L.SetField(budgetTable, "spent", L.NewFunction(func(state *glua.LState) int {
		if err := checkCall(); err != nil {
			state.RaiseError("%s", err.Error())
			return 0
		}
		days := state.CheckInt(1)
		if days < 1 || days > 3650 {
			state.RaiseError("days must be between 1 and 3650")
			return 0
		}
		curr := snap.TargetCurrency
		if state.GetTop() >= 2 {
			curr = state.CheckString(2)
		}
		var total int64
		if snap.OutflowsPast != nil {
			arr := snap.OutflowsPast[curr]
			if len(arr) > 0 {
				idx := days
				if idx > len(arr) {
					idx = len(arr)
				}
				total = arr[idx-1]
			}
		}
		state.Push(glua.LNumber(total))
		return 1
	}))
	L.SetGlobal("budget", budgetTable)

	// window table
	windowTable := L.NewTable()
	L.SetField(windowTable, "days", glua.LNumber(snap.WindowDays))
	L.SetGlobal("window", windowTable)
}

func luaMathAbs(L *glua.LState) int {
	n := L.CheckNumber(1)
	L.Push(glua.LNumber(math.Abs(float64(n))))
	return 1
}

func luaMathCeil(L *glua.LState) int {
	n := L.CheckNumber(1)
	L.Push(glua.LNumber(math.Ceil(float64(n))))
	return 1
}

func luaMathFloor(L *glua.LState) int {
	n := L.CheckNumber(1)
	L.Push(glua.LNumber(math.Floor(float64(n))))
	return 1
}

func luaMathFmod(L *glua.LState) int {
	x := L.CheckNumber(1)
	y := L.CheckNumber(2)
	L.Push(glua.LNumber(math.Mod(float64(x), float64(y))))
	return 1
}

func luaMathMax(L *glua.LState) int {
	top := L.GetTop()
	if top == 0 {
		return 0
	}
	max := float64(L.CheckNumber(1))
	for i := 2; i <= top; i++ {
		v := float64(L.CheckNumber(i))
		if v > max {
			max = v
		}
	}
	L.Push(glua.LNumber(max))
	return 1
}

func luaMathMin(L *glua.LState) int {
	top := L.GetTop()
	if top == 0 {
		return 0
	}
	min := float64(L.CheckNumber(1))
	for i := 2; i <= top; i++ {
		v := float64(L.CheckNumber(i))
		if v < min {
			min = v
		}
	}
	L.Push(glua.LNumber(min))
	return 1
}

func luaMathPow(L *glua.LState) int {
	x := L.CheckNumber(1)
	y := L.CheckNumber(2)
	L.Push(glua.LNumber(math.Pow(float64(x), float64(y))))
	return 1
}

func luaMathSqrt(L *glua.LState) int {
	n := L.CheckNumber(1)
	L.Push(glua.LNumber(math.Sqrt(float64(n))))
	return 1
}

func luaToNumber(L *glua.LState) int {
	val := L.Get(1)
	switch v := val.(type) {
	case glua.LNumber:
		L.Push(v)
		return 1
	case glua.LString:
		var f float64
		if _, err := fmt.Sscanf(string(v), "%f", &f); err == nil {
			L.Push(glua.LNumber(f))
			return 1
		}
	}
	L.Push(glua.LNil)
	return 1
}

func registerTextLib(L *glua.LState, checkCall func() error) {
	textTable := L.NewTable()
	L.SetField(textTable, "contains", L.NewFunction(func(state *glua.LState) int {
		if err := checkCall(); err != nil {
			state.RaiseError("%s", err.Error())
			return 0
		}
		s := state.CheckString(1)
		substr := state.CheckString(2)
		state.Push(glua.LBool(strings.Contains(s, substr)))
		return 1
	}))
	L.SetField(textTable, "starts_with", L.NewFunction(func(state *glua.LState) int {
		if err := checkCall(); err != nil {
			state.RaiseError("%s", err.Error())
			return 0
		}
		s := state.CheckString(1)
		prefix := state.CheckString(2)
		state.Push(glua.LBool(strings.HasPrefix(s, prefix)))
		return 1
	}))
	L.SetField(textTable, "ends_with", L.NewFunction(func(state *glua.LState) int {
		if err := checkCall(); err != nil {
			state.RaiseError("%s", err.Error())
			return 0
		}
		s := state.CheckString(1)
		suffix := state.CheckString(2)
		state.Push(glua.LBool(strings.HasSuffix(s, suffix)))
		return 1
	}))
	L.SetField(textTable, "lower", L.NewFunction(func(state *glua.LState) int {
		if err := checkCall(); err != nil {
			state.RaiseError("%s", err.Error())
			return 0
		}
		s := state.CheckString(1)
		state.Push(glua.LString(strings.ToLower(s)))
		return 1
	}))
	L.SetField(textTable, "upper", L.NewFunction(func(state *glua.LState) int {
		if err := checkCall(); err != nil {
			state.RaiseError("%s", err.Error())
			return 0
		}
		s := state.CheckString(1)
		state.Push(glua.LString(strings.ToUpper(s)))
		return 1
	}))
	L.SetField(textTable, "trim", L.NewFunction(func(state *glua.LState) int {
		if err := checkCall(); err != nil {
			state.RaiseError("%s", err.Error())
			return 0
		}
		s := state.CheckString(1)
		state.Push(glua.LString(strings.TrimSpace(s)))
		return 1
	}))
	L.SetField(textTable, "replace", L.NewFunction(func(state *glua.LState) int {
		if err := checkCall(); err != nil {
			state.RaiseError("%s", err.Error())
			return 0
		}
		s := state.CheckString(1)
		oldS := state.CheckString(2)
		newS := state.CheckString(3)
		res := strings.ReplaceAll(s, oldS, newS)
		if len(res) > 512 {
			res = res[:512]
		}
		state.Push(glua.LString(res))
		return 1
	}))
	L.SetField(textTable, "join", L.NewFunction(func(state *glua.LState) int {
		if err := checkCall(); err != nil {
			state.RaiseError("%s", err.Error())
			return 0
		}
		tbl := state.CheckTable(1)
		sep := state.OptString(2, "")
		var parts []string
		tbl.ForEach(func(k, v glua.LValue) {
			parts = append(parts, v.String())
		})
		res := strings.Join(parts, sep)
		if len(res) > 512 {
			res = res[:512]
		}
		state.Push(glua.LString(res))
		return 1
	}))
	L.SetField(textTable, "regex_match", L.NewFunction(func(state *glua.LState) int {
		if err := checkCall(); err != nil {
			state.RaiseError("%s", err.Error())
			return 0
		}
		pattern := state.CheckString(1)
		s := state.CheckString(2)
		re, err := regexp.Compile(pattern)
		if err != nil {
			state.Push(glua.LBool(false))
			return 1
		}
		state.Push(glua.LBool(re.MatchString(s)))
		return 1
	}))
	L.SetGlobal("text", textTable)
}

func registerDateLib(L *glua.LState, checkCall func() error) {
	dateTable := L.NewTable()
	parseDateArg := func(val glua.LValue) time.Time {
		switch v := val.(type) {
		case glua.LNumber:
			n := int64(v)
			if n > 1e14 {
				return time.UnixMicro(n).UTC()
			}
			return time.Unix(n, 0).UTC()
		case glua.LString:
			str := string(v)
			if t, err := time.Parse("2006-01-02", str); err == nil {
				return t.UTC()
			}
			if t, err := time.Parse(time.RFC3339, str); err == nil {
				return t.UTC()
			}
		}
		return time.Now().UTC()
	}

	L.SetField(dateTable, "year", L.NewFunction(func(state *glua.LState) int {
		if err := checkCall(); err != nil {
			state.RaiseError("%s", err.Error())
			return 0
		}
		t := parseDateArg(state.Get(1))
		state.Push(glua.LNumber(t.Year()))
		return 1
	}))
	L.SetField(dateTable, "month", L.NewFunction(func(state *glua.LState) int {
		if err := checkCall(); err != nil {
			state.RaiseError("%s", err.Error())
			return 0
		}
		t := parseDateArg(state.Get(1))
		state.Push(glua.LNumber(int(t.Month())))
		return 1
	}))
	L.SetField(dateTable, "day", L.NewFunction(func(state *glua.LState) int {
		if err := checkCall(); err != nil {
			state.RaiseError("%s", err.Error())
			return 0
		}
		t := parseDateArg(state.Get(1))
		state.Push(glua.LNumber(t.Day()))
		return 1
	}))
	L.SetField(dateTable, "weekday", L.NewFunction(func(state *glua.LState) int {
		if err := checkCall(); err != nil {
			state.RaiseError("%s", err.Error())
			return 0
		}
		t := parseDateArg(state.Get(1))
		state.Push(glua.LString(t.Weekday().String()))
		return 1
	}))
	L.SetField(dateTable, "format", L.NewFunction(func(state *glua.LState) int {
		if err := checkCall(); err != nil {
			state.RaiseError("%s", err.Error())
			return 0
		}
		t := parseDateArg(state.Get(1))
		layout := state.OptString(2, "2006-01-02")
		state.Push(glua.LString(t.Format(layout)))
		return 1
	}))
	L.SetField(dateTable, "parse", L.NewFunction(func(state *glua.LState) int {
		if err := checkCall(); err != nil {
			state.RaiseError("%s", err.Error())
			return 0
		}
		str := state.CheckString(1)
		if t, err := time.Parse("2006-01-02", str); err == nil {
			state.Push(glua.LNumber(t.UnixMicro()))
			return 1
		}
		if t, err := time.Parse(time.RFC3339, str); err == nil {
			state.Push(glua.LNumber(t.UnixMicro()))
			return 1
		}
		state.Push(glua.LNumber(0))
		return 1
	}))
	L.SetGlobal("date", dateTable)
}

func parseMinorValue(val glua.LValue) (int64, error) {
	switch v := val.(type) {
	case glua.LNumber:
		return int64(math.Round(float64(v))), nil
	case glua.LString:
		var f float64
		if _, err := fmt.Sscanf(string(v), "%f", &f); err == nil {
			return int64(math.Round(f * 100)), nil
		}
		return 0, fmt.Errorf("invalid numeric string: %s", string(v))
	default:
		return 0, fmt.Errorf("expected number or numeric string")
	}
}

func registerAutomationTx(
	L *glua.LState,
	tx AutomationTxContext,
	snap *InMemoryBudgetSnapshot,
	checkCall func() error,
	readOnly bool,
	outcome *AutomationOutcome,
) {
	txTable := L.NewTable()

	notesStr := ""
	if tx.Notes != nil {
		notesStr = *tx.Notes
	}
	bucketIDStr := ""
	if tx.BucketID != nil {
		bucketIDStr = *tx.BucketID
	}
	targetIDStr := ""
	if tx.TargetID != nil {
		targetIDStr = *tx.TargetID
	}

	dateStr := ""
	if tx.TransactionDate > 0 {
		dateStr = time.UnixMicro(tx.TransactionDate).UTC().Format("2006-01-02")
	}

	L.SetField(txTable, "id", glua.LString(tx.ID))
	L.SetField(txTable, "notes", glua.LString(notesStr))
	L.SetField(txTable, "amount", glua.LNumber(tx.SourceAmountMinor))
	L.SetField(txTable, "source_amount", glua.LNumber(tx.SourceAmountMinor))
	L.SetField(txTable, "dest_amount", glua.LNumber(tx.DestAmountMinor))
	L.SetField(txTable, "currency", glua.LString(tx.SourceCurrency))
	L.SetField(txTable, "source_currency", glua.LString(tx.SourceCurrency))
	L.SetField(txTable, "dest_currency", glua.LString(tx.DestCurrency))
	L.SetField(txTable, "date", glua.LString(dateStr))
	L.SetField(txTable, "date_epoch", glua.LNumber(tx.TransactionDate))

	L.SetField(txTable, "party", glua.LString(tx.PartyName))
	L.SetField(txTable, "party_id", glua.LString(tx.PartyID))
	L.SetField(txTable, "source_account", glua.LString(tx.SourceAccountName))
	L.SetField(txTable, "source_account_id", glua.LString(tx.SourceAccountID))
	L.SetField(txTable, "dest_account", glua.LString(tx.DestAccountName))
	L.SetField(txTable, "dest_account_id", glua.LString(tx.DestinationAccountID))

	L.SetField(txTable, "bucket", glua.LString(tx.BucketName))
	L.SetField(txTable, "bucket_id", glua.LString(bucketIDStr))
	L.SetField(txTable, "target", glua.LString(tx.TargetName))
	L.SetField(txTable, "target_id", glua.LString(targetIDStr))

	L.SetField(txTable, "is_outflow", glua.LBool(tx.IsUserOwnedSource && !tx.IsUserOwnedDest))
	L.SetField(txTable, "is_inflow", glua.LBool(!tx.IsUserOwnedSource && tx.IsUserOwnedDest))
	L.SetField(txTable, "is_transfer", glua.LBool(tx.IsUserOwnedSource && tx.IsUserOwnedDest))

	if !readOnly && outcome != nil {
		L.SetField(txTable, "set_bucket", L.NewFunction(func(state *glua.LState) int {
			if err := checkCall(); err != nil {
				state.RaiseError("%s", err.Error())
				return 0
			}
			val := strings.TrimSpace(state.OptString(1, ""))
			if val == "" {
				outcome.UpdatedTx.BucketID = nil
				outcome.UpdatedTx.BucketName = ""
				outcome.Modified = true
				return 0
			}
			var found *BucketMeta
			if snap != nil && snap.Buckets != nil {
				found = snap.Buckets[val]
				if found == nil {
					found = snap.Buckets[strings.ToLower(val)]
				}
			}
			if found != nil {
				outcome.UpdatedTx.BucketID = &found.ID
				outcome.UpdatedTx.BucketName = found.Name
				outcome.Modified = true
			} else {
				// Accept direct UUID if valid
				outcome.UpdatedTx.BucketID = &val
				outcome.UpdatedTx.BucketName = val
				outcome.Modified = true
			}
			return 0
		}))

		L.SetField(txTable, "set_target", L.NewFunction(func(state *glua.LState) int {
			if err := checkCall(); err != nil {
				state.RaiseError("%s", err.Error())
				return 0
			}
			val := strings.TrimSpace(state.OptString(1, ""))
			if val == "" {
				outcome.UpdatedTx.TargetID = nil
				outcome.UpdatedTx.TargetName = ""
				outcome.Modified = true
				return 0
			}
			var found *TargetMeta
			if snap != nil && snap.Targets != nil {
				found = snap.Targets[val]
				if found == nil {
					found = snap.Targets[strings.ToLower(val)]
				}
			}
			if found != nil {
				outcome.UpdatedTx.TargetID = &found.ID
				outcome.UpdatedTx.TargetName = found.Name
				outcome.Modified = true
				if found.BucketID != "" {
					outcome.UpdatedTx.BucketID = &found.BucketID
					if snap != nil && snap.Buckets != nil {
						if b, ok := snap.Buckets[found.BucketID]; ok {
							outcome.UpdatedTx.BucketName = b.Name
						}
					}
				}
			} else {
				outcome.UpdatedTx.TargetID = &val
				outcome.UpdatedTx.TargetName = val
				outcome.Modified = true
			}
			return 0
		}))

		L.SetField(txTable, "set_party", L.NewFunction(func(state *glua.LState) int {
			if err := checkCall(); err != nil {
				state.RaiseError("%s", err.Error())
				return 0
			}
			val := strings.TrimSpace(state.OptString(1, ""))
			if val == "" {
				outcome.UpdatedTx.PartyID = ""
				outcome.UpdatedTx.PartyName = ""
				outcome.Modified = true
				return 0
			}
			var found *PartyMeta
			if snap != nil && snap.Parties != nil {
				found = snap.Parties[val]
				if found == nil {
					found = snap.Parties[strings.ToLower(val)]
				}
			}
			if found != nil {
				outcome.UpdatedTx.PartyID = found.ID
				outcome.UpdatedTx.PartyName = found.Name
				outcome.Modified = true
			} else {
				outcome.UpdatedTx.PartyID = val
				outcome.UpdatedTx.PartyName = val
				outcome.Modified = true
			}
			return 0
		}))

		L.SetField(txTable, "set_source_account", L.NewFunction(func(state *glua.LState) int {
			if err := checkCall(); err != nil {
				state.RaiseError("%s", err.Error())
				return 0
			}
			val := strings.TrimSpace(state.CheckString(1))
			var found *AccountMeta
			if snap != nil && snap.Accounts != nil {
				found = snap.Accounts[val]
				if found == nil {
					found = snap.Accounts[strings.ToLower(val)]
				}
			}
			if found != nil {
				outcome.UpdatedTx.SourceAccountID = found.ID
				outcome.UpdatedTx.SourceAccountName = found.Name
				outcome.UpdatedTx.IsUserOwnedSource = found.IsUserOwned
				outcome.Modified = true
			} else {
				outcome.UpdatedTx.SourceAccountID = val
				outcome.UpdatedTx.SourceAccountName = val
				outcome.Modified = true
			}
			return 0
		}))

		L.SetField(txTable, "set_dest_account", L.NewFunction(func(state *glua.LState) int {
			if err := checkCall(); err != nil {
				state.RaiseError("%s", err.Error())
				return 0
			}
			val := strings.TrimSpace(state.CheckString(1))
			var found *AccountMeta
			if snap != nil && snap.Accounts != nil {
				found = snap.Accounts[val]
				if found == nil {
					found = snap.Accounts[strings.ToLower(val)]
				}
			}
			if found != nil {
				outcome.UpdatedTx.DestinationAccountID = found.ID
				outcome.UpdatedTx.DestAccountName = found.Name
				outcome.UpdatedTx.IsUserOwnedDest = found.IsUserOwned
				outcome.Modified = true
			} else {
				outcome.UpdatedTx.DestinationAccountID = val
				outcome.UpdatedTx.DestAccountName = val
				outcome.Modified = true
			}
			return 0
		}))

		L.SetField(txTable, "set_notes", L.NewFunction(func(state *glua.LState) int {
			if err := checkCall(); err != nil {
				state.RaiseError("%s", err.Error())
				return 0
			}
			s := state.CheckString(1)
			if len(s) > 1024 {
				s = s[:1024]
			}
			outcome.UpdatedTx.Notes = &s
			outcome.Modified = true
			return 0
		}))

		L.SetField(txTable, "set_amount", L.NewFunction(func(state *glua.LState) int {
			if err := checkCall(); err != nil {
				state.RaiseError("%s", err.Error())
				return 0
			}
			minor, err := parseMinorValue(state.Get(1))
			if err != nil {
				state.RaiseError("set_amount: %s", err.Error())
				return 0
			}
			outcome.UpdatedTx.SourceAmountMinor = minor
			if outcome.UpdatedTx.SourceCurrency == outcome.UpdatedTx.DestCurrency {
				outcome.UpdatedTx.DestAmountMinor = minor
			}
			outcome.Modified = true
			return 0
		}))

		L.SetField(txTable, "set_source_amount", L.NewFunction(func(state *glua.LState) int {
			if err := checkCall(); err != nil {
				state.RaiseError("%s", err.Error())
				return 0
			}
			minor, err := parseMinorValue(state.Get(1))
			if err != nil {
				state.RaiseError("set_source_amount: %s", err.Error())
				return 0
			}
			outcome.UpdatedTx.SourceAmountMinor = minor
			outcome.Modified = true
			return 0
		}))

		L.SetField(txTable, "set_dest_amount", L.NewFunction(func(state *glua.LState) int {
			if err := checkCall(); err != nil {
				state.RaiseError("%s", err.Error())
				return 0
			}
			minor, err := parseMinorValue(state.Get(1))
			if err != nil {
				state.RaiseError("set_dest_amount: %s", err.Error())
				return 0
			}
			outcome.UpdatedTx.DestAmountMinor = minor
			outcome.Modified = true
			return 0
		}))

		L.SetField(txTable, "set_date", L.NewFunction(func(state *glua.LState) int {
			if err := checkCall(); err != nil {
				state.RaiseError("%s", err.Error())
				return 0
			}
			val := state.Get(1)
			var epochMicro int64
			switch v := val.(type) {
			case glua.LNumber:
				n := int64(v)
				if n > 1e14 {
					epochMicro = n
				} else {
					epochMicro = n * 1e6
				}
			case glua.LString:
				if t, err := time.Parse("2006-01-02", string(v)); err == nil {
					epochMicro = t.UTC().UnixMicro()
				} else if t, err := time.Parse(time.RFC3339, string(v)); err == nil {
					epochMicro = t.UTC().UnixMicro()
				} else {
					state.RaiseError("set_date: invalid date format (expected YYYY-MM-DD)")
					return 0
				}
			default:
				state.RaiseError("set_date: expected string or numeric timestamp")
				return 0
			}
			outcome.UpdatedTx.TransactionDate = epochMicro
			outcome.Modified = true
			return 0
		}))

		L.SetField(txTable, "delete", L.NewFunction(func(state *glua.LState) int {
			if err := checkCall(); err != nil {
				state.RaiseError("%s", err.Error())
				return 0
			}
			outcome.Deleted = true
			outcome.Modified = true
			return 0
		}))

		L.SetField(txTable, "split", L.NewFunction(func(state *glua.LState) int {
			if err := checkCall(); err != nil {
				state.RaiseError("%s", err.Error())
				return 0
			}
			splitsTbl := state.CheckTable(1)
			outcome.Splits = nil
			var parseErr error

			splitsTbl.ForEach(func(k, v glua.LValue) {
				if parseErr != nil {
					return
				}
				itemTbl, ok := v.(*glua.LTable)
				if !ok {
					parseErr = fmt.Errorf("each split item must be a table")
					return
				}
				amtVal := itemTbl.RawGetString("amount")
				amtMinor, err := parseMinorValue(amtVal)
				if err != nil {
					parseErr = fmt.Errorf("split amount: %w", err)
					return
				}

				var bucketID *string
				bucketVal := strings.TrimSpace(itemTbl.RawGetString("bucket").String())
				if bucketVal != "" && bucketVal != "nil" {
					if snap != nil && snap.Buckets != nil {
						if b := snap.Buckets[bucketVal]; b != nil {
							bucketID = &b.ID
						} else if b := snap.Buckets[strings.ToLower(bucketVal)]; b != nil {
							bucketID = &b.ID
						} else {
							bucketID = &bucketVal
						}
					} else {
						bucketID = &bucketVal
					}
				}

				var targetID *string
				targetVal := strings.TrimSpace(itemTbl.RawGetString("target").String())
				if targetVal != "" && targetVal != "nil" {
					if snap != nil && snap.Targets != nil {
						if t := snap.Targets[targetVal]; t != nil {
							targetID = &t.ID
							if t.BucketID != "" && bucketID == nil {
								bucketID = &t.BucketID
							}
						} else if t := snap.Targets[strings.ToLower(targetVal)]; t != nil {
							targetID = &t.ID
							if t.BucketID != "" && bucketID == nil {
								bucketID = &t.BucketID
							}
						} else {
							targetID = &targetVal
						}
					} else {
						targetID = &targetVal
					}
				}

				var notes *string
				notesVal := itemTbl.RawGetString("notes").String()
				if notesVal != "" && notesVal != "nil" {
					if len(notesVal) > 512 {
						notesVal = notesVal[:512]
					}
					notes = &notesVal
				}

				outcome.Splits = append(outcome.Splits, SplitItem{
					AmountMinor: amtMinor,
					BucketID:    bucketID,
					TargetID:    targetID,
					Notes:       notes,
				})
			})

			if parseErr != nil {
				state.RaiseError("tx.split error: %s", parseErr.Error())
				return 0
			}
			outcome.Modified = true
			return 0
		}))
	}

	L.SetGlobal("tx", txTable)
}

// EvaluateAutomationJudge executes the judge script against a transaction context.
func EvaluateAutomationJudge(ctx context.Context, script string, txCtx AutomationTxContext, snap *InMemoryBudgetSnapshot) (bool, error) {
	trimmed := strings.TrimSpace(script)
	if !strings.HasPrefix(trimmed, "return ") && !strings.HasPrefix(trimmed, "if ") && !strings.Contains(trimmed, "\n") {
		trimmed = "return " + trimmed
	}
	if err := ValidateLuaScript(trimmed); err != nil {
		return false, fmt.Errorf("judge script validation error: %w", err)
	}

	ctx, cancel := context.WithTimeout(ctx, EmergencyTimeout)
	defer cancel()

	L := glua.NewState(glua.Options{
		CallStackSize:   16,
		RegistrySize:    128,
		RegistryMaxSize: 256,
		SkipOpenLibs:    true,
	})
	defer L.Close()
	L.SetContext(ctx)

	hostCallCount := 0
	checkHostCall := func() error {
		hostCallCount++
		if hostCallCount > MaxHostAPICalls {
			return ErrHostCallLimitExceeded
		}
		return nil
	}

	registerSandboxedEnvironment(L, snap, checkHostCall)
	registerTextLib(L, checkHostCall)
	registerDateLib(L, checkHostCall)
	registerAutomationTx(L, txCtx, snap, checkHostCall, true, nil)

	if err := L.DoString(trimmed); err != nil {
		return false, fmt.Errorf("judge execution error: %w", err)
	}

	top := L.GetTop()
	if top < 1 {
		return false, nil
	}
	val := L.Get(top)
	if b, ok := val.(glua.LBool); ok {
		return bool(b), nil
	}
	return false, nil
}

// EvaluateAutomationExecutor executes the executor script against a transaction context.
func EvaluateAutomationExecutor(ctx context.Context, script string, txCtx AutomationTxContext, snap *InMemoryBudgetSnapshot) (*AutomationOutcome, error) {
	if err := ValidateLuaScript(script); err != nil {
		return nil, fmt.Errorf("executor script validation error: %w", err)
	}

	ctx, cancel := context.WithTimeout(ctx, EmergencyTimeout)
	defer cancel()

	L := glua.NewState(glua.Options{
		CallStackSize:   16,
		RegistrySize:    128,
		RegistryMaxSize: 256,
		SkipOpenLibs:    true,
	})
	defer L.Close()
	L.SetContext(ctx)

	hostCallCount := 0
	checkHostCall := func() error {
		hostCallCount++
		if hostCallCount > MaxHostAPICalls {
			return ErrHostCallLimitExceeded
		}
		return nil
	}

	outcome := &AutomationOutcome{
		UpdatedTx: txCtx,
	}

	registerSandboxedEnvironment(L, snap, checkHostCall)
	registerTextLib(L, checkHostCall)
	registerDateLib(L, checkHostCall)
	registerAutomationTx(L, txCtx, snap, checkHostCall, false, outcome)

	if err := L.DoString(script); err != nil {
		return nil, fmt.Errorf("executor execution error: %w", err)
	}

	if len(outcome.Splits) > 0 {
		if len(outcome.Splits) < 2 {
			return nil, errors.New("tx.split requires at least 2 split parts")
		}
		var totalMinor int64
		for _, sp := range outcome.Splits {
			if sp.AmountMinor <= 0 {
				return nil, errors.New("split parts must have positive amounts")
			}
			totalMinor += sp.AmountMinor
		}
		if totalMinor != txCtx.SourceAmountMinor {
			return nil, fmt.Errorf("split amounts total (%d) does not match original transaction amount (%d)", totalMinor, txCtx.SourceAmountMinor)
		}
	}

	return outcome, nil
}
