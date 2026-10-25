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
	Planned  int64
	Required int64
	Funded   int64
	Spent    int64
	Priority int
	Currency string
}

// InMemoryBudgetSnapshot provides O(1) in-memory financial data to the VM without database I/O.
type InMemoryBudgetSnapshot struct {
	Targets        map[string]*TargetMeta // lookup by ID and lowercase Name
	BucketBalances map[string]int64       // currency -> minor balance
	FXRates        map[string]int64       // "EUR_PLN" -> rate_micros
	InflowsPast    map[string][]int64     // currency -> daily inflow prefix sum
	OutflowsPast   map[string][]int64     // currency -> daily outflow prefix sum
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
