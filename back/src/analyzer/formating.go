package analyzer

import (
	"fmt"

	"github.com/fatih/color"
)

func (r TaskResult) String() string {
	return spf("%s (%s)  %s (%s)  [%s%s  %s%s]  %s",
		clr(spf("+%7.2f%%", (r.Coef-1)*100), color.FgHiGreen),
		clr(spf("%3d", r.Wins), color.FgHiGreen),

		clr(spf("%5.2f%%", r.MaxDrawdown), color.FgHiRed),
		clr(spf("%3d", r.Losses), color.FgHiRed),

		clr("pr/dd:", color.FgWhite),
		clr(spf("%5.2f", r.ProfitToDd), color.FgHiCyan),

		clr("pr/cnd:", color.FgWhite),
		clr(spf("%6.4f", r.ProfitToCandles), color.FgHiYellow),

		r.Task,
	)
}

func (t Task) String() string {
	return spf("%s  %s",
		t.IndicatorsCompare,
		t.TpSlParam,
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
