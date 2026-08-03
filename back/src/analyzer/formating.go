package analyzer

import (
	"fmt"
	"math"

	"github.com/fatih/color"
)

func (r TaskResult) String() string {
	return spf("%s (%s)  %s (%s)  [%s%s  %s%s]  %s  %s",
		clr(spf("+%7.2f%%", (r.Coef-1)*100), color.FgHiGreen),
		clr(spf("%3d", r.Wins), color.FgHiGreen),

		clr(spf("%5.2f%%", r.MaxDrawdown), color.FgHiRed),
		clr(spf("%3d", r.Losses), color.FgHiRed),

		clr("pr/dd:", color.FgWhite),
		clr(spf("%5.2f", r.ProfitToDd), color.FgHiCyan),

		clr("pr/cnd:", color.FgWhite),
		clr(spf("%6.4f", r.ProfitToCandles), color.FgHiYellow),

		r.Task,
		r.Task.Url(),
	)
}

func round2(val float64) float64 {
	return math.Round(val*100) / 100
}

func (t Task) Url() string {
	tpVal := t.TpSlParam.Tp
	slVal := t.TpSlParam.Sl
	if t.Decimals > 0 {
		factor := math.Pow10(t.Decimals)
		tpVal *= factor
		slVal *= factor
	}
	return spf("http://localhost/?tf=%s&tp=%g&sl=%g&i1=%s,%g,%s&i2=%s,%g,%s",
		t.Timeframe,
		round2(tpVal),
		round2(slVal),
		t.IndicatorsCompare.Indicator1Params.Type,
		round2(float64(t.IndicatorsCompare.Indicator1Params.Coef)),
		t.IndicatorsCompare.Indicator1Params.Source,
		t.IndicatorsCompare.Indicator2Params.Type,
		round2(float64(t.IndicatorsCompare.Indicator2Params.Coef)),
		t.IndicatorsCompare.Indicator2Params.Source,
	)
}

func (t Task) String() string {
	tpVal := t.TpSlParam.Tp
	slVal := t.TpSlParam.Sl
	if t.Decimals > 0 {
		factor := math.Pow10(t.Decimals)
		tpVal *= factor
		slVal *= factor
	}
	return spf("%s  %s%s  %s%s",
		t.IndicatorsCompare,
		clr("tp:", color.FgWhite),
		clr(spf("%g", round2(tpVal)), color.FgHiGreen),
		clr("sl:", color.FgWhite),
		clr(spf("%g", round2(slVal)), color.FgHiRed),
	)
}

func (c IndicatorsCompare) String() string {
	return spf("%s[%s]  %s[%s]",
		clr("ind1:", color.FgWhite),
		c.Indicator1Params,

		clr("ind2:", color.FgWhite),
		c.Indicator2Params,
	)
}

func (p IndicatorParams) String() string {
	return spf("%s %3s %s",
		clr(spf("%8s", p.Type), color.FgHiMagenta),
		p.Source,
		clr(spf("%5.2f", p.Coef), color.FgHiCyan),
	)
}

func (p TpSlParam) String() string {
	return spf("%s%s  %s%s",
		clr("tp:", color.FgWhite),
		clr(spf("%7.4f", p.Tp), color.FgHiGreen),

		clr("sl:", color.FgWhite),
		clr(spf("%7.4f", p.Sl), color.FgHiRed),
	)
}

func clr(text string, attrs ...color.Attribute) string {
	c := color.New(attrs...)
	c.EnableColor()
	return c.Sprintf("%s", text)
}

func spf(f string, a ...any) string {
	return fmt.Sprintf(f, a...)
}
