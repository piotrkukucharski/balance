//go:build external_integration

package ai

import (
	"context"
	"os"
	"testing"
)

func TestLiveGeminiBudgetStructure_ComplexPrompt(t *testing.T) {
	apiKey := os.Getenv("GEMINI_API_KEY")
	if apiKey == "" {
		t.Skip("GEMINI_API_KEY not set")
	}

	model := os.Getenv("GEMINI_MODEL")
	if model == "" {
		model = "gemini-3.8-flash"
	}

	provider := NewGeminiBudgetStructureProvider(apiKey, model, ResolveGeminiTimeout())

	prompt := `Internet 100 zł co miesiąc, angielski 60 złotych co tydzień, terapia 180 złotych co tydzień, siłownia 160 zł co tydzień, karta miejsca 125 zł co 3 miesiące, święta bożego narodzenia 1000 wydany do grudnia 24, google ai pro 100 zł miesięcznie, Miasto Jest Nasze 50zł miesięczne najbliższe pobranie 22.11.2026, Ekonomia i cała reszta 10zł miesięczne najbliższe pobranie 22.11.2026, Myśleć Głębiej 9zł miesięczne najbliższe pobranie 14.11.2026, Neuropa 10zł miesięczne najbliższe pobranie 05.11.2026,  Horyzontalny Klub Dyskusyjny 25zł miesięczne najbliższe pobranie 04.11.2026, Samiec Beta 10zł miesięczne najbliższe pobranie 02.11.2026, Mao Powiedziane 5zł miesięczne najbliższe pobranie 27.10.2026, Mistycyzm Popkulturowy 25zł miesięczne najbliższe pobranie 27.10.2026, Apple One 49,99 zł miesięczne najbliższe pobranie 29 października, Drafts 99,99 zł roczne najbliższe pobranie 19 grudnia, DSDownload Station 37,99 zł roczne najbliższe pobranie 12.03.2027, Infuse 47,99 zł roczne najbliższe pobranie 28.02.2027, kitshn 9,99 zł roczne najbliższe pobranie 03.07.2027, Merriam-Webster Dictionary 28,99 zł roczne najbliższe pobranie 31.07.2027, Parcel 19,99 zł roczne najbliższe pobranie 08.03.2027, PhotoSync 29,99 zł roczne najbliższe pobranie 05.03.2027, Tinder 589,99 zł roczne najbliższe pobranie 04.10.2027,`

	res, err := provider.GenerateBudgetStructure(context.Background(), BudgetStructurePromptRequest{
		Prompt:          prompt,
		DefaultCurrency: "PLN",
	})
	if err != nil {
		t.Fatalf("GenerateBudgetStructure failed: %v", err)
	}

	if len(res.Groups) == 0 {
		t.Fatalf("expected groups, got 0")
	}

	totalBuckets := 0
	totalTargets := 0
	for _, g := range res.Groups {
		totalBuckets += len(g.Buckets)
		for _, b := range g.Buckets {
			totalTargets += len(b.Targets)
		}
	}

	t.Logf("Successfully generated structure with %d groups, %d buckets, %d targets", len(res.Groups), totalBuckets, totalTargets)
	if totalTargets < 15 {
		t.Errorf("expected at least 15 targets from prompt, got %d", totalTargets)
	}
}
