package analyzer

import (
	"fmt"

	"github.com/fatih/color"
)

func (r TaskResult) String() string {
	return spf("pr:%s dd:%s [pr/dd:%s pr/cnd:%s] %s",
		clr(spf("+%6.2f%%", (r.Coef-1)*100), color.FgHiGreen),
		clr(spf("%5.2f%%", r.MaxDrawdown), color.FgHiRed),
		clr(spf("%5.2f", r.ProfitToDd), color.FgHiCyan),
		clr(spf("%6.4f", r.ProfitToCandles), color.FgHiYellow),
		r.Task,
	)
}

func (t Task) String() string {
	return spf("%s %s",
		t.IndicatorsCompare,
		t.TpSlParam,
	)
}

func (c IndicatorsCompare) String() string {
	return spf("ind1:[%s] ind2:[%s]", c.Indicator1Params, c.Indicator2Params)
}

func (p IndicatorParams) String() string {
	return spf("%s %3s %s",
		clr(spf("%8s", p.Type), color.FgHiYellow),
		p.Source,
		clr(spf("%5.2f", p.Coef), color.FgHiCyan),
	)
}

func (p TpSlParam) String() string {
	return spf("%s %s",
		clr(spf("tp:%6.4f", p.Tp), color.FgHiGreen),
		clr(spf("sl:%6.4f", p.Sl), color.FgHiRed),
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
