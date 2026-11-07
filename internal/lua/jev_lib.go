package lua

import (
	"context"
	"fmt"
	"time"

	"github.com/balance/balance/internal/jev"
	glua "github.com/yuin/gopher-lua"
)

// MaxJevCalls is the maximum number of Jev System One calls allowed per script run.
const MaxJevCalls = 3

// ErrJevCallLimitExceeded is returned when a script attempts to invoke Jev primitives more than MaxJevCalls times.
const ErrJevCallLimitExceeded = "jev call quota exceeded (max 3 calls)"

func registerJevLib(ctx context.Context, L *glua.LState, client jev.JevClient) {
	if client == nil {
		return
	}

	callCount := 0
	jevTable := L.NewTable()

	// jev.choice(question, candidates, contextData) -> (result, confidence, err)
	L.SetField(jevTable, "choice", L.NewFunction(func(state *glua.LState) int {
		callCount++
		if callCount > MaxJevCalls {
			state.Push(glua.LNil)
			state.Push(glua.LNumber(0.0))
			state.Push(glua.LString(ErrJevCallLimitExceeded))
			return 3
		}

		question := state.CheckString(1)
		candTbl := state.CheckTable(2)
		var candidates []string
		candTbl.ForEach(func(_, v glua.LValue) {
			if s, ok := v.(glua.LString); ok {
				candidates = append(candidates, string(s))
			} else if n, ok := v.(glua.LNumber); ok {
				candidates = append(candidates, fmt.Sprintf("%v", n))
			}
		})

		var contextData map[string]any
		if state.GetTop() >= 3 && state.Get(3).Type() == glua.LTTable {
			contextData = ltableToMap(state.Get(3).(*glua.LTable))
		} else {
			contextData = make(map[string]any)
		}

		// Enforce per-call 1.0s timeout bounded by parent context
		callCtx, cancel := context.WithTimeout(ctx, 1000*time.Millisecond)
		defer cancel()

		result, confidence, err := client.Choice(callCtx, question, candidates, contextData)
		if err != nil {
			state.Push(glua.LNil)
			state.Push(glua.LNumber(0.0))
			state.Push(glua.LString(err.Error()))
			return 3
		}

		state.Push(glua.LString(result))
		state.Push(glua.LNumber(confidence))
		state.Push(glua.LNil)
		return 3
	}))

	// jev.noul(question, contextData) -> (probability, confidence, err)
	L.SetField(jevTable, "noul", L.NewFunction(func(state *glua.LState) int {
		callCount++
		if callCount > MaxJevCalls {
			state.Push(glua.LNil)
			state.Push(glua.LNumber(0.0))
			state.Push(glua.LString(ErrJevCallLimitExceeded))
			return 3
		}

		question := state.CheckString(1)
		var contextData map[string]any
		if state.GetTop() >= 2 && state.Get(2).Type() == glua.LTTable {
			contextData = ltableToMap(state.Get(2).(*glua.LTable))
		} else {
			contextData = make(map[string]any)
		}

		callCtx, cancel := context.WithTimeout(ctx, 1000*time.Millisecond)
		defer cancel()

		prob, confidence, err := client.Noul(callCtx, question, contextData)
		if err != nil {
			state.Push(glua.LNil)
			state.Push(glua.LNumber(0.0))
			state.Push(glua.LString(err.Error()))
			return 3
		}

		state.Push(glua.LNumber(prob))
		state.Push(glua.LNumber(confidence))
		state.Push(glua.LNil)
		return 3
	}))

	L.SetGlobal("jev", jevTable)
}

func ltableToMap(tbl *glua.LTable) map[string]any {
	res := make(map[string]any)
	tbl.ForEach(func(k, v glua.LValue) {
		key := fmt.Sprintf("%v", k)
		res[key] = lvalueToGo(v)
	})
	return res
}

func lvalueToGo(val glua.LValue) any {
	switch v := val.(type) {
	case glua.LBool:
		return bool(v)
	case glua.LNumber:
		return float64(v)
	case glua.LString:
		return string(v)
	case *glua.LTable:
		// Check if it's an array or dictionary
		max := v.MaxN()
		if max > 0 {
			arr := make([]any, 0, max)
			for i := 1; i <= max; i++ {
				arr = append(arr, lvalueToGo(v.RawGetInt(i)))
			}
			return arr
		}
		return ltableToMap(v)
	default:
		return nil
	}
}
